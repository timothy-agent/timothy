package channels

import (
	"context"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/SumonMSelim/timothy/internal/brain/chat"
	"github.com/SumonMSelim/timothy/internal/brain/events"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// Reply copy.
const (
	msgPairingPrompt = "This bot answers only paired users. Ask the operator to approve you in Timothy (Settings > Channels), or send the code shown there."
	msgPaired        = "Paired. Say hello."
	msgTextOnly      = "Text only for now."
	msgTooLong       = "That message is too long (max 4000 characters). Please send a shorter one."
	msgSlowDown      = "Slow down: at most 10 messages a minute."
	msgQueueFull     = "Still working on your earlier messages, try again in a moment."
	msgThinking      = "Thinking..."
	msgNoReply       = "(no reply)"
	msgWaitingNote   = "\n\nWaiting for your approval in Timothy (web)."
	msgStarted       = "Started: "
	failureCap       = 300
)

const (
	conflictBackoff = 30 * time.Second
	minBackoff      = time.Second
	maxBackoff      = 60 * time.Second
	authLogEvery    = 10 * time.Minute
)

// turnJob is one queued message of a conversation.
type turnJob struct {
	conv        Conversation
	to          target
	text        string
	attachments []chat.AttachmentRef
}

// runner receives from one channel's adapter and runs its inbound
// pipeline. The receive loop is single-goroutine; turns run on one
// worker per conversation.
type runner struct {
	svc    *Service
	ch     Channel
	ad     adapter
	me     identity
	limits *limiter

	workers map[string]chan turnJob
	wg      sync.WaitGroup

	backoff        time.Duration
	conflictLogged bool
	lastAuthLog    time.Time
}

func newRunner(s *Service, c Channel, ad adapter) *runner {
	return &runner{svc: s, ch: c, ad: ad, limits: newLimiter(), workers: map[string]chan turnJob{}}
}

func (r *runner) run(ctx context.Context) {
	defer r.wg.Wait()
	for {
		me, err := r.ad.connect(ctx)
		if err == nil {
			r.me, r.backoff = me, 0
			break
		}
		if !r.sleep(ctx, r.pause(ctx, err)) {
			return
		}
	}
	var cursor string
	for {
		st, err := r.svc.store.GetState(ctx, r.ch.ID)
		if err == nil {
			cursor = st.Cursor
			break
		}
		if !r.sleep(ctx, r.pause(ctx, err)) {
			return
		}
	}
	for ctx.Err() == nil {
		items, next, err := r.ad.receive(ctx, cursor)
		if err != nil {
			if !r.sleep(ctx, r.pause(ctx, err)) {
				return
			}
			continue
		}
		r.backoff, r.conflictLogged = 0, false
		if failed := r.handleBatch(ctx, items); failed != nil {
			// The cursor stays put so the batch is received again;
			// dedup skips what was handled.
			if !r.sleep(ctx, r.pause(ctx, failed)) {
				return
			}
			continue
		}
		if next != cursor {
			if err := r.svc.store.SetState(ctx, r.ch.ID, State{Cursor: next}); err != nil && ctx.Err() == nil {
				r.svc.log.Warn("channels: save cursor failed", "channel_id", r.ch.ID, "error", err)
			}
			cursor = next
		}
	}
}

// handleBatch handles items in order; a store failure stops the batch.
func (r *runner) handleBatch(ctx context.Context, items []inbound) error {
	for _, in := range items {
		if err := r.handle(ctx, in); err != nil {
			return err
		}
	}
	return nil
}

// pause logs err and returns how long to wait before the next call.
func (r *runner) pause(ctx context.Context, err error) time.Duration {
	if ctx.Err() != nil {
		return 0
	}
	switch apiStatus(err) {
	case 409:
		if !r.conflictLogged {
			r.conflictLogged = true
			r.svc.log.Warn("channels: another poller is using this bot token", "channel_id", r.ch.ID)
		}
		return conflictBackoff
	case 401:
		if now := r.svc.now(); now.Sub(r.lastAuthLog) >= authLogEvery {
			r.lastAuthLog = now
			r.svc.log.Warn("channels: bot token rejected", "channel_id", r.ch.ID, "error", err)
		}
	default:
		r.svc.log.Warn("channels: poll failed", "channel_id", r.ch.ID, "error", err)
	}
	r.backoff = min(max(r.backoff*2, minBackoff), maxBackoff)
	if wait := retryAfter(err); wait > r.backoff {
		return min(wait, maxBackoff)
	}
	return r.backoff
}

func (r *runner) sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// handle runs one item through dedup, addressing, pairing, rate
// limit, input checks, automation triggers and the conversation queue.
// Only store failures return an error.
func (r *runner) handle(ctx context.Context, in inbound) error {
	fresh, err := r.svc.store.MarkInbound(ctx, r.ch.ID, in.DedupID)
	if err != nil || !fresh {
		return err
	}
	if in.Press != nil {
		return r.handlePress(ctx, in)
	}
	if !in.Addressed && in.MaybeThread {
		key := conversationKey(in)
		_, found, err := r.svc.store.ConversationFor(ctx, r.ch.ID, key.ChatID, key.ThreadID)
		if err != nil {
			return err
		}
		in.Addressed = found
	}
	if !in.Addressed {
		return nil
	}
	now := r.svc.now()
	p, _, err := r.svc.store.EnsurePairing(ctx, r.ch.ID, in.UserID, in.DisplayName, now)
	if err != nil {
		return err
	}
	step := decidePairing(p, in.Text, now)
	if step == stepRedeem {
		redeemed, err := r.svc.store.RedeemCode(ctx, r.ch.ID, in.UserID, strings.TrimSpace(in.Text), now)
		if err != nil {
			return err
		}
		if redeemed {
			r.reply(ctx, in, msgPaired)
			return nil
		}
		p.Code = ""
		step = decidePairing(p, in.Text, now)
	}
	switch step {
	case stepContinue:
	case stepPrompt:
		code, err := newCode()
		if err != nil {
			return err
		}
		if err := r.svc.store.IssueCode(ctx, r.ch.ID, in.UserID, code, now.Add(codeTTL), now); err != nil {
			return err
		}
		r.reply(ctx, in, msgPairingPrompt)
		return nil
	default:
		return nil
	}
	if ok, warn := r.limits.allow(r.ch.ID+":"+in.UserID, now); !ok {
		if warn {
			r.reply(ctx, in, msgSlowDown)
		}
		return nil
	}
	if strings.TrimSpace(in.Text) == "" && len(in.Attachments) == 0 {
		r.reply(ctx, in, msgTextOnly)
		return nil
	}
	if utf8.RuneCountInString(in.Text) > maxInputChars {
		r.reply(ctx, in, msgTooLong)
		return nil
	}
	key := conversationKey(in)
	conv, found, err := r.svc.store.ConversationFor(ctx, r.ch.ID, key.ChatID, key.ThreadID)
	if err != nil {
		return err
	}
	if found && in.ReplyToID != "" && !r.ad.caps().Buttons {
		data, pressed, err := r.svc.store.TakeButton(ctx, conv.ID, in.ReplyToID, in.Text)
		if err != nil {
			return err
		}
		if pressed {
			in.Press = &press{Data: data, MessageID: in.ReplyToID}
			return r.handlePress(ctx, in)
		}
	}
	if found && in.ReplyToID != "" {
		missionID, kind, asked, err := r.svc.store.TakeAsk(ctx, conv.ID, in.ReplyToID)
		if err != nil {
			return err
		}
		if asked {
			r.answerAsk(ctx, in, missionID, kind)
			return nil
		}
	}
	trigger, matched, err := r.matchTrigger(ctx, in)
	if err != nil {
		return err
	}
	if !found {
		conv, err = r.svc.store.CreateConversation(ctx, Conversation{
			ChannelID: r.ch.ID, ExternalChatID: key.ChatID, ExternalThreadID: key.ThreadID, ExternalUserID: in.UserID, AgentID: r.ch.AgentID,
		}, kindLabel(r.ch.Kind)+": "+in.DisplayName)
		if err != nil {
			return err
		}
	}
	if matched {
		return r.startAutomation(ctx, in, conv, trigger)
	}
	select {
	case r.worker(ctx, conv.ID) <- turnJob{conv: conv, to: key, text: in.Text, attachments: in.Attachments}:
	default:
		r.reply(ctx, in, msgQueueFull)
	}
	return nil
}

// matchTrigger returns the first channel trigger in's text fires; more
// than one match is logged and only the first fires.
func (r *runner) matchTrigger(ctx context.Context, in inbound) (ChannelTrigger, bool, error) {
	ts, err := r.svc.channelTriggers(ctx, r.ch.ID)
	if err != nil {
		return ChannelTrigger{}, false, err
	}
	text := strings.TrimSpace(in.Text)
	var first ChannelTrigger
	n := 0
	for _, t := range ts {
		if t.Pattern == nil || (t.ChatID != "" && t.ChatID != in.ChatID) || !t.Pattern.MatchString(text) {
			continue
		}
		if n == 0 {
			first = t
		}
		n++
	}
	if n > 1 {
		r.svc.log.Info("channels: message matched several automations, the first fires", "channel_id", r.ch.ID, "matched", n, "automation_id", first.AutomationID)
	}
	return first, n > 0, nil
}

// startAutomation records in as a channel.message event for trigger t
// instead of a chat turn and confirms the start. A duplicate delivery
// inserts nothing and stays silent.
func (r *runner) startAutomation(ctx context.Context, in inbound, conv Conversation, t ChannelTrigger) error {
	ev, err := events.ChannelMessage(events.ChannelMessagePayload{
		ChannelID: r.ch.ID, ConversationID: conv.ID, ChatID: in.ChatID, ThreadID: conv.ExternalThreadID, UserID: in.UserID,
		Sender: in.DisplayName, MessageID: in.MessageID, Text: strings.TrimSpace(in.Text), TriggerID: t.TriggerID,
	}, in.DedupID)
	if err != nil {
		return err
	}
	_, inserted, err := r.svc.inbox(ctx, ev)
	if err != nil || !inserted {
		return err
	}
	if r.svc.kickRuns != nil {
		r.svc.kickRuns()
	}
	r.svc.log.Info("channels: automation triggered", "channel_id", r.ch.ID, "conversation_id", conv.ID, "automation_id", t.AutomationID, "trigger_id", t.TriggerID)
	r.reply(ctx, in, msgStarted+t.AutomationName)
	return nil
}

func (r *runner) reply(ctx context.Context, in inbound, text string) {
	if _, err := r.ad.send(ctx, conversationKey(in), text, nil, false); err != nil && ctx.Err() == nil {
		r.svc.log.Warn("channels: reply failed", "channel_id", r.ch.ID, "error", err)
	}
}

// worker returns the queue of a conversation, starting its goroutine
// on first use.
func (r *runner) worker(ctx context.Context, convID string) chan turnJob {
	if q, ok := r.workers[convID]; ok {
		return q
	}
	q := make(chan turnJob, queueDepth)
	r.workers[convID] = q
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case j := <-q:
				r.turn(ctx, j)
			}
		}
	}()
	return q
}

// turn posts a placeholder, runs the chat turn and streams it into
// the placeholder. Without edits there is no placeholder: the final
// reply is sent once.
func (r *runner) turn(ctx context.Context, j turnJob) {
	start := r.svc.now()
	edits := r.ad.caps().Edits
	var msgID string
	if edits {
		id, err := r.ad.send(ctx, j.to, msgThinking, nil, true)
		if err != nil {
			if ctx.Err() == nil {
				r.svc.log.Warn("channels: placeholder failed", "channel_id", r.ch.ID, "conversation_id", j.conv.ID, "error", err)
			}
			return
		}
		msgID = id
	}
	agent := j.conv.AgentID
	if agent == "" && r.ch.Config.Dispatch {
		agent = chat.AutoAgent
	}
	var reply string
	_, events, err := r.svc.chat(ctx, chat.Request{SessionID: j.conv.SessionID, Message: j.text, Agent: agent, Attachments: j.attachments})
	if err != nil {
		r.show(ctx, j, msgID, failureText(err.Error()))
	} else if edits {
		every := r.svc.editEvery
		if every == 0 {
			every = r.ad.caps().EditEvery
		}
		ticker := time.NewTicker(every)
		reply = r.drain(ctx, j, msgID, events, ticker.C)
		ticker.Stop()
	} else {
		reply = r.drain(ctx, j, "", events, nil)
	}
	if err := r.svc.store.TouchConversation(ctx, j.conv.ID, r.svc.now()); err != nil && ctx.Err() == nil {
		r.svc.log.Warn("channels: touch conversation failed", "conversation_id", j.conv.ID, "error", err)
	}
	r.svc.log.Info("channels: turn", "channel_id", r.ch.ID, "conversation_id", j.conv.ID, "session_id", j.conv.SessionID,
		"in_chars", utf8.RuneCountInString(j.text), "out_chars", utf8.RuneCountInString(reply), "duration_ms", r.svc.now().Sub(start).Milliseconds())
}

// drain consumes a turn's stream: text accumulates, each tick edits
// the placeholder when the view changed, a permission ask appends a
// note, and the terminal event renders the final reply. msgID "" means
// no placeholder: nothing is edited and the final reply is sent. It
// returns the reply text.
func (r *runner) drain(ctx context.Context, j turnJob, msgID string, events <-chan stream.StreamEvent, tick <-chan time.Time) string {
	var text strings.Builder
	throttle := editThrottle{window: streamWindowFor(r.ad.caps().MessageLimit)}
	waiting := false
	// buttons maps a permission id to its buttons message.
	buttons := map[string]string{}
	view := func() string {
		if waiting {
			return text.String() + msgWaitingNote
		}
		return text.String()
	}
	for {
		select {
		case <-ctx.Done():
			return text.String()
		case <-tick:
			if v, ok := throttle.due(view()); ok {
				r.edit(ctx, j.to, msgID, v)
			}
		case ev, open := <-events:
			if !open {
				r.finish(ctx, j, msgID, text.String())
				return text.String()
			}
			switch ev.Type {
			case stream.EventChunk:
				text.WriteString(ev.Text)
			case stream.EventPermissionRequest:
				waiting = true
				if v, ok := throttle.due(view()); ok && msgID != "" {
					r.edit(ctx, j.to, msgID, v)
				}
				if p := ev.Permission; p != nil && p.ID != "" && r.svc.missions.ResolvePermission != nil {
					id, err := r.svc.sendButtons(ctx, r.ad, j.conv.ID, j.to, permText("Timothy", p.Tool, p.Rationale), permKeyboard(p.ID))
					if err != nil {
						if ctx.Err() == nil {
							r.svc.log.Warn("channels: permission buttons failed", "channel_id", r.ch.ID, "conversation_id", j.conv.ID, "error", err)
						}
					} else {
						buttons[p.ID] = id
					}
				}
			case stream.EventPermissionResolved:
				waiting = false
				if res := ev.Resolved; res != nil {
					if id, ok := buttons[res.ID]; ok {
						delete(buttons, res.ID)
						if err := r.ad.edit(ctx, j.to, id, decisionText(res.Decision), nil); err != nil && ctx.Err() == nil {
							r.svc.log.Warn("channels: close buttons failed", "channel_id", r.ch.ID, "error", err)
						}
					}
				}
			case stream.EventDone:
				r.finish(ctx, j, msgID, text.String())
				return text.String()
			case stream.EventError:
				msg := "unknown error"
				if ev.Err != nil && ev.Err.Message != "" {
					msg = ev.Err.Message
				}
				r.show(ctx, j, msgID, failureText(msg))
				return text.String()
			}
		}
	}
}

// finish replaces the placeholder with the first chunk of the reply
// and sends the rest as new messages; without a placeholder every
// chunk is sent.
func (r *runner) finish(ctx context.Context, j turnJob, msgID string, text string) {
	if strings.TrimSpace(text) == "" {
		text = msgNoReply
	}
	chunks := chunkReply(text, r.ad.caps().MessageLimit)
	if msgID != "" {
		r.edit(ctx, j.to, msgID, chunks[0])
		chunks = chunks[1:]
	}
	for _, c := range chunks {
		if _, err := r.ad.send(ctx, j.to, c, nil, false); err != nil {
			if ctx.Err() == nil {
				r.svc.log.Warn("channels: send chunk failed", "channel_id", r.ch.ID, "conversation_id", j.conv.ID, "error", err)
			}
			return
		}
	}
}

// show puts text in the placeholder, or sends it when there is none.
func (r *runner) show(ctx context.Context, j turnJob, msgID, text string) {
	if msgID != "" {
		r.edit(ctx, j.to, msgID, text)
		return
	}
	if _, err := r.ad.send(ctx, j.to, text, nil, false); err != nil && ctx.Err() == nil {
		r.svc.log.Warn("channels: send failed", "channel_id", r.ch.ID, "conversation_id", j.conv.ID, "error", err)
	}
}

func (r *runner) edit(ctx context.Context, to target, msgID string, text string) {
	if err := r.ad.edit(ctx, to, msgID, text, nil); err != nil && ctx.Err() == nil {
		r.svc.log.Warn("channels: edit failed", "channel_id", r.ch.ID, "error", err)
	}
}

func failureText(msg string) string {
	if utf8.RuneCountInString(msg) > failureCap {
		msg = string([]rune(msg)[:failureCap])
	}
	return "Something went wrong: " + msg
}
