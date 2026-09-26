package channels

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/attachments"
	"github.com/SumonMSelim/timothy/internal/brain/chat"
	"github.com/SumonMSelim/timothy/internal/brain/connectors"
	"github.com/SumonMSelim/timothy/internal/brain/events"
	"github.com/SumonMSelim/timothy/internal/brain/loop"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

const (
	// reconcileEvery re-checks the channels switch and rows between
	// change notifications.
	reconcileEvery = time.Minute
	sweepEvery     = time.Hour
	// editEvery is Telegram's streaming edit cadence.
	editEvery = 2 * time.Second
	// retryEvery re-runs a reconcile that could not list channels.
	retryEvery = 5 * time.Second
	// parkEvery re-lists parked missions; hub signals are droppable
	// hints.
	parkEvery = time.Minute
	// triggerTTL is how long a channel's trigger list is cached.
	triggerTTL = 30 * time.Second
)

// ChatFunc runs one chat turn; production passes chat.Service.Chat.
type ChatFunc func(ctx context.Context, req chat.Request) (string, <-chan stream.StreamEvent, error)

// MissionDeps are the mission and permission calls buttons, parks and
// outcomes use. Every field is nil-safe: an unset one disables that
// feature.
type MissionDeps struct {
	Get               func(ctx context.Context, id string) (missions.Mission, error)
	Events            func(ctx context.Context, id string) ([]missions.Event, error)
	AppendEvent       func(ctx context.Context, id, kind string, payload map[string]any) error
	ListParked        func(ctx context.Context) ([]missions.Mission, error)
	Signal            func(ctx context.Context, id string, input missions.Input) error
	DecidePlan        func(ctx context.Context, id string, input missions.Input, feedback string) error
	AnswerAskUser     func(ctx context.Context, id, answer string) error
	ResolvePermission func(ctx context.Context, id, decision string) bool
	PendingPermission func(ctx context.Context, id string) (loop.PendingPermission, bool, error)
	Subscribe         func(ctx context.Context) <-chan missions.Signal
	WebBaseURL        func(ctx context.Context) string
}

// Service runs one adapter per enabled channel and restarts them when
// channel rows change.
type Service struct {
	store         *Store
	chat          ChatFunc
	missions      MissionDeps
	resolveSecret func(ctx context.Context, ref string) (string, error)
	http          *http.Client
	log           *slog.Logger
	now           func() time.Time
	// APIBase and SlackAPIBase override the transport API base URLs
	// for tests; empty uses the real APIs.
	APIBase      string
	SlackAPIBase string
	// editEvery overrides the adapter's edit cadence for tests; 0 uses
	// the adapter's.
	editEvery  time.Duration
	retryEvery time.Duration
	parkEvery  time.Duration
	// mail wires email channels; SetEmail fills its mailbox.
	mail emailEnv
	// triggers, inbox and kick start automations from messages;
	// SetTriggers fills them, nil turns matching off.
	triggers func(ctx context.Context, channelID string) ([]ChannelTrigger, error)
	inbox    func(ctx context.Context, ev events.Event) (int64, bool, error)
	kickRuns func()
	trigMu   sync.Mutex
	trigList map[string]cachedTriggers

	reload chan struct{}
	// enabled is the channels switch Run was given (nil runs always).
	enabled func(context.Context) bool

	// parked maps a mission id to the park key last pushed; skipLogged
	// marks missions whose push was skipped (disabled channel). Watcher
	// goroutine only.
	parked     map[string]string
	skipLogged map[string]bool

	mu      sync.Mutex
	runners map[string]*runnerHandle
	running int // runner count after the last reconcile, -1 before it
}

// ChannelTrigger is one automation channel trigger a message can fire.
type ChannelTrigger struct {
	TriggerID      string
	AutomationID   string
	AutomationName string
	Pattern        *regexp.Regexp
	// ChatID limits the trigger to one chat when set.
	ChatID string
}

type cachedTriggers struct {
	at   time.Time
	list []ChannelTrigger
}

type runnerHandle struct {
	updatedAt time.Time
	cancel    context.CancelFunc
	done      chan struct{}
}

// New wires the service and subscribes it to store changes.
func New(store *Store, chatFn ChatFunc, deps MissionDeps, resolveSecret func(ctx context.Context, ref string) (string, error), client *http.Client, log *slog.Logger) *Service {
	s := &Service{
		store: store, chat: chatFn, missions: deps, resolveSecret: resolveSecret, http: client, log: log,
		now: time.Now, retryEvery: retryEvery, parkEvery: parkEvery,
		reload: make(chan struct{}, 1), runners: map[string]*runnerHandle{}, running: -1,
		parked: map[string]string{}, skipLogged: map[string]bool{}, trigList: map[string]cachedTriggers{},
	}
	s.mail = emailEnv{store: store, log: log, now: func() time.Time { return s.now() }}
	store.SetOnChange(func(context.Context) { s.kick() })
	return s
}

func (s *Service) kick() {
	select {
	case s.reload <- struct{}{}:
	default:
	}
}

// SetEmail enables email channels: open returns an imap connector's
// mailbox, save stores attachments (nil skips them) and poll is the
// inbox poll interval (floored at 30 s). Call before Run.
func (s *Service) SetEmail(open func(ctx context.Context, id string) (*connectors.IMAPMailbox, error),
	save func(ctx context.Context, r io.Reader) (attachments.Attachment, error), poll func(ctx context.Context) time.Duration) {
	s.mail.mailbox, s.mail.save = openMailbox(open), save
	s.mail.poll = func(ctx context.Context) time.Duration {
		if poll == nil {
			return emailPollDefault
		}
		return max(poll(ctx), emailPollFloor)
	}
}

// SetTriggers lets messages start automations: list returns a
// channel's triggers (cached triggerTTL), inbox records the matched
// message (events.Store.AddIfNew) and kick asks the drainer to consume
// it. Call before Run.
func (s *Service) SetTriggers(list func(ctx context.Context, channelID string) ([]ChannelTrigger, error),
	inbox func(ctx context.Context, ev events.Event) (int64, bool, error), kick func()) {
	s.triggers, s.inbox, s.kickRuns = list, inbox, kick
}

// channelTriggers returns channel id's triggers from the cache, listing
// them again once triggerTTL has passed.
func (s *Service) channelTriggers(ctx context.Context, id string) ([]ChannelTrigger, error) {
	if s.triggers == nil || s.inbox == nil {
		return nil, nil
	}
	now := s.now()
	s.trigMu.Lock()
	c, ok := s.trigList[id]
	s.trigMu.Unlock()
	if ok && now.Sub(c.at) < triggerTTL {
		return c.list, nil
	}
	list, err := s.triggers(ctx, id)
	if err != nil {
		return nil, err
	}
	s.trigMu.Lock()
	s.trigList[id] = cachedTriggers{at: now, list: list}
	s.trigMu.Unlock()
	return list, nil
}

func (s *Service) adapterFor(c Channel) (adapter, error) {
	return newAdapter(c, s.http, s.resolveSecret, s.APIBase, s.SlackAPIBase, &s.mail)
}

// sendButtons sends text with buttons, rendering them as a reply hint
// on transports without buttons and remembering them on conversation
// convID so a reply presses one.
func (s *Service) sendButtons(ctx context.Context, ad adapter, convID string, to target, text string, buttons [][]button) (string, error) {
	if len(buttons) == 0 || ad.caps().Buttons {
		return ad.send(ctx, to, text, buttons, false)
	}
	id, err := ad.send(ctx, to, text+buttonsText(buttons), nil, false)
	if err != nil || convID == "" {
		return id, err
	}
	if err := s.store.RememberButtons(ctx, convID, id, buttons); err != nil {
		s.log.Warn("channels: remember buttons failed", "conversation_id", convID, "error", err)
	}
	return id, nil
}

// Test checks the channel's credentials (Telegram getMe, Slack
// auth.test plus apps.connections.open, the email mailbox's INBOX),
// records the username (the mailbox address for email) and returns it.
func (s *Service) Test(ctx context.Context, id string) (string, error) {
	c, err := s.store.Get(ctx, id)
	if err != nil {
		return "", err
	}
	ad, err := s.adapterFor(c)
	if err != nil {
		return "", err
	}
	connect := ad.connect
	if sa, ok := ad.(*slackAdapter); ok {
		connect = sa.verify
	}
	me, err := connect(ctx)
	if err != nil {
		return "", err
	}
	if err := s.store.SetBotUsername(ctx, id, me.Username); err != nil {
		return "", err
	}
	return me.Username, nil
}

// Run reconciles runners until ctx is done. enabled is the channels
// switch (nil runs always).
func (s *Service) Run(ctx context.Context, enabled func(context.Context) bool) {
	s.enabled = enabled
	if s.missions.Get != nil && (s.missions.Subscribe != nil || s.missions.ListParked != nil) {
		done := make(chan struct{})
		defer func() { <-done }()
		go func() {
			defer close(done)
			s.watchParks(ctx)
		}()
	}
	reconcile := time.NewTicker(reconcileEvery)
	defer reconcile.Stop()
	sweep := time.NewTicker(sweepEvery)
	defer sweep.Stop()
	var retry <-chan time.Time
	run := func() {
		retry = nil
		if !s.reconcile(ctx, enabled) {
			retry = time.After(s.retryEvery)
		}
	}
	run()
	for {
		select {
		case <-ctx.Done():
			s.stopAll()
			return
		case <-s.reload:
			run()
		case <-reconcile.C:
			run()
		case <-retry:
			run()
		case <-sweep.C:
			if err := s.store.Sweep(ctx, s.now()); err != nil && ctx.Err() == nil {
				s.log.Warn("channels: sweep failed", "error", err)
			}
		}
	}
}

// reconcile stops runners whose channel is gone, disabled or changed
// and starts runners for enabled channels. It returns false when the
// channels could not be listed.
func (s *Service) reconcile(ctx context.Context, enabled func(context.Context) bool) bool {
	want := map[string]Channel{}
	if enabled == nil || enabled(ctx) {
		rows, err := s.store.List(ctx)
		if err != nil {
			if ctx.Err() == nil {
				s.log.Warn("channels: list failed", "error", err)
			}
			return false
		}
		for _, c := range rows {
			if c.Enabled && (c.Kind == KindTelegram || c.Kind == KindSlack || c.Kind == KindEmail) {
				want[c.ID] = c
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, h := range s.runners {
		if c, ok := want[id]; ok && c.UpdatedAt.Equal(h.updatedAt) {
			continue
		}
		h.cancel()
		<-h.done
		delete(s.runners, id)
		s.log.Info("channels: stopped", "channel_id", id)
	}
	for id, c := range want {
		if _, ok := s.runners[id]; ok {
			continue
		}
		ad, err := s.adapterFor(c)
		if err != nil {
			continue
		}
		rctx, cancel := context.WithCancel(ctx)
		h := &runnerHandle{updatedAt: c.UpdatedAt, cancel: cancel, done: make(chan struct{})}
		r := newRunner(s, c, ad)
		go func() {
			defer close(h.done)
			r.run(rctx)
		}()
		s.runners[id] = h
		s.log.Info("channels: started", "channel_id", id, "kind", c.Kind)
	}
	if len(s.runners) == 0 && s.running != 0 {
		s.log.Info("channels: no channels enabled")
	}
	s.running = len(s.runners)
	return true
}

func (s *Service) stopAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, h := range s.runners {
		h.cancel()
		<-h.done
		delete(s.runners, id)
	}
}
