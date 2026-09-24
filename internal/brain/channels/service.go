package channels

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/chat"
	"github.com/SumonMSelim/timothy/internal/brain/loop"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

const (
	// reconcileEvery re-checks the channels switch and rows between
	// change notifications.
	reconcileEvery = time.Minute
	sweepEvery     = time.Hour
	// editEvery is the streaming edit cadence.
	editEvery = 2 * time.Second
	// retryEvery re-runs a reconcile that could not list channels.
	retryEvery = 5 * time.Second
	// parkEvery re-lists parked missions; hub signals are droppable
	// hints.
	parkEvery = time.Minute
)

// ChatFunc runs one chat turn; production passes chat.Service.Chat.
type ChatFunc func(ctx context.Context, req chat.Request) (string, <-chan stream.StreamEvent, error)

// MissionDeps are the mission and permission calls buttons, parks and
// outcomes use. Every field is nil-safe: an unset one disables that
// feature.
type MissionDeps struct {
	Get               func(ctx context.Context, id string) (missions.Mission, error)
	Events            func(ctx context.Context, id string) ([]missions.Event, error)
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
	// APIBase overrides the Bot API base URL for tests; empty uses the
	// real API.
	APIBase    string
	editEvery  time.Duration
	retryEvery time.Duration
	parkEvery  time.Duration

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

type runnerHandle struct {
	updatedAt time.Time
	cancel    context.CancelFunc
	done      chan struct{}
}

// New wires the service and subscribes it to store changes.
func New(store *Store, chatFn ChatFunc, deps MissionDeps, resolveSecret func(ctx context.Context, ref string) (string, error), client *http.Client, log *slog.Logger) *Service {
	s := &Service{
		store: store, chat: chatFn, missions: deps, resolveSecret: resolveSecret, http: client, log: log,
		now: time.Now, editEvery: editEvery, retryEvery: retryEvery, parkEvery: parkEvery,
		reload: make(chan struct{}, 1), runners: map[string]*runnerHandle{}, running: -1,
		parked: map[string]string{}, skipLogged: map[string]bool{},
	}
	store.SetOnChange(func(context.Context) { s.kick() })
	return s
}

func (s *Service) kick() {
	select {
	case s.reload <- struct{}{}:
	default:
	}
}

func (s *Service) bot(credentialRef string) *botAPI {
	base := s.APIBase
	if base == "" {
		base = defaultAPIBase
	}
	return &botAPI{http: s.http, base: base, resolve: s.resolveSecret, ref: credentialRef}
}

// Test calls getMe with the channel's token, records the username and
// returns it.
func (s *Service) Test(ctx context.Context, id string) (string, error) {
	c, err := s.store.Get(ctx, id)
	if err != nil {
		return "", err
	}
	me, err := s.bot(c.CredentialRef).getMe(ctx)
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
// and starts runners for enabled telegram channels. It returns false
// when the channels could not be listed.
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
			if c.Enabled && c.Kind == KindTelegram {
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
		rctx, cancel := context.WithCancel(ctx)
		h := &runnerHandle{updatedAt: c.UpdatedAt, cancel: cancel, done: make(chan struct{})}
		r := newTelegramRunner(s, c)
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
