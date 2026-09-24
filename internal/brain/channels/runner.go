package channels

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/SumonMSelim/timothy/internal/brain/chat"
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
	conv     Conversation
	chatID   int64
	threadID int64
	text     string
}

// telegramRunner long-polls one Telegram channel and runs its inbound
// pipeline. The poll loop is single-goroutine; turns run on one worker
// per conversation.
type telegramRunner struct {
	svc    *Service
	ch     Channel
	bot    *botAPI
	me     botIdentity
	limits *limiter

	workers map[string]chan turnJob
	wg      sync.WaitGroup

	backoff        time.Duration
	conflictLogged bool
	lastAuthLog    time.Time
}

func newTelegramRunner(s *Service, c Channel) *telegramRunner {
	return &telegramRunner{svc: s, ch: c, bot: s.bot(c.CredentialRef), limits: newLimiter(), workers: map[string]chan turnJob{}}
}

func (r *telegramRunner) run(ctx context.Context) {
	defer r.wg.Wait()
	for {
		me, err := r.bot.getMe(ctx)
		if err == nil {
			r.me, r.backoff = me, 0
			break
		}
		if !r.sleep(ctx, r.pause(ctx, err)) {
			return
		}
	}
	var offset int64
	for {
		st, err := r.svc.store.GetState(ctx, r.ch.ID)
		if err == nil {
			offset = st.UpdateOffset
			break
		}
		if !r.sleep(ctx, r.pause(ctx, err)) {
			return
		}
	}
	for ctx.Err() == nil {
		updates, err := r.bot.getUpdates(ctx, offset)
		if err != nil {
			if !r.sleep(ctx, r.pause(ctx, err)) {
				return
			}
			continue
		}
		r.backoff, r.conflictLogged = 0, false
		next, failed := r.handleBatch(ctx, updates, offset)
		if next != offset {
			if err := r.svc.store.SetState(ctx, r.ch.ID, State{UpdateOffset: next}); err != nil && ctx.Err() == nil {
				r.svc.log.Warn("channels: save offset failed", "channel_id", r.ch.ID, "error", err)
			}
			offset = next
		}
		if failed != nil && !r.sleep(ctx, r.pause(ctx, failed)) {
			return
		}
	}
}

// handleBatch handles updates in order and returns the offset after
// the last fully handled one; a store failure stops the batch so the
// rest is fetched again (dedup makes that safe).
func (r *telegramRunner) handleBatch(ctx context.Context, updates []tgUpdate, offset int64) (int64, error) {
	for _, u := range updates {
		if err := r.handle(ctx, u); err != nil {
			return offset, err
		}
		offset = u.UpdateID + 1
	}
	return offset, nil
}

// pause logs err and returns how long to wait before the next call.
func (r *telegramRunner) pause(ctx context.Context, err error) time.Duration {
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
	return r.backoff
}

func (r *telegramRunner) sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// handle runs one update through dedup, addressing, pairing, rate
// limit, input checks and the conversation queue. Only store failures
// return an error.
func (r *telegramRunner) handle(ctx context.Context, u tgUpdate) error {
	fresh, err := r.svc.store.MarkInbound(ctx, r.ch.ID, "telegram:"+strconv.FormatInt(u.UpdateID, 10))
	if err != nil || !fresh {
		return err
	}
	in, ok := parseUpdate(u, r.me)
	if !ok || !in.Addressed {
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
	if strings.TrimSpace(in.Text) == "" {
		r.reply(ctx, in, msgTextOnly)
		return nil
	}
	if utf8.RuneCountInString(in.Text) > maxInputChars {
		r.reply(ctx, in, msgTooLong)
		return nil
	}
	chatID, threadID := conversationKey(in)
	conv, found, err := r.svc.store.ConversationFor(ctx, r.ch.ID, chatID, threadID)
	if err != nil {
		return err
	}
	if !found {
		conv, err = r.svc.store.CreateConversation(ctx, Conversation{
			ChannelID: r.ch.ID, ExternalChatID: chatID, ExternalThreadID: threadID, ExternalUserID: in.UserID, AgentID: r.ch.AgentID,
		}, "Telegram: "+in.DisplayName)
		if err != nil {
			return err
		}
	}
	select {
	case r.worker(ctx, conv.ID) <- turnJob{conv: conv, chatID: in.ChatID, threadID: in.ThreadID, text: in.Text}:
	default:
		r.reply(ctx, in, msgQueueFull)
	}
	return nil
}

func (r *telegramRunner) reply(ctx context.Context, in inbound, text string) {
	if _, err := r.bot.sendMessage(ctx, in.ChatID, in.ThreadID, text, false); err != nil && ctx.Err() == nil {
		r.svc.log.Warn("channels: reply failed", "channel_id", r.ch.ID, "error", err)
	}
}

// worker returns the queue of a conversation, starting its goroutine
// on first use.
func (r *telegramRunner) worker(ctx context.Context, convID string) chan turnJob {
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
// the placeholder.
func (r *telegramRunner) turn(ctx context.Context, j turnJob) {
	start := r.svc.now()
	msgID, err := r.bot.sendMessage(ctx, j.chatID, j.threadID, msgThinking, true)
	if err != nil {
		if ctx.Err() == nil {
			r.svc.log.Warn("channels: placeholder failed", "channel_id", r.ch.ID, "conversation_id", j.conv.ID, "error", err)
		}
		return
	}
	agent := j.conv.AgentID
	if agent == "" && r.ch.Config.Dispatch {
		agent = chat.AutoAgent
	}
	var reply string
	_, events, err := r.svc.chat(ctx, chat.Request{SessionID: j.conv.SessionID, Message: j.text, Agent: agent})
	if err != nil {
		r.edit(ctx, j.chatID, msgID, failureText(err.Error()))
	} else {
		ticker := time.NewTicker(r.svc.editEvery)
		reply = r.drain(ctx, j, msgID, events, ticker.C)
		ticker.Stop()
	}
	if err := r.svc.store.TouchConversation(ctx, j.conv.ID, r.svc.now()); err != nil && ctx.Err() == nil {
		r.svc.log.Warn("channels: touch conversation failed", "conversation_id", j.conv.ID, "error", err)
	}
	r.svc.log.Info("channels: turn", "channel_id", r.ch.ID, "conversation_id", j.conv.ID, "session_id", j.conv.SessionID,
		"in_chars", utf8.RuneCountInString(j.text), "out_chars", utf8.RuneCountInString(reply), "duration_ms", r.svc.now().Sub(start).Milliseconds())
}

// drain consumes a turn's stream: text accumulates, each tick edits
// the placeholder when the view changed, a permission ask appends a
// note, and the terminal event renders the final reply. It returns
// the reply text.
func (r *telegramRunner) drain(ctx context.Context, j turnJob, msgID int64, events <-chan stream.StreamEvent, tick <-chan time.Time) string {
	var text strings.Builder
	var throttle editThrottle
	waiting := false
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
				r.edit(ctx, j.chatID, msgID, v)
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
				if v, ok := throttle.due(view()); ok {
					r.edit(ctx, j.chatID, msgID, v)
				}
			case stream.EventPermissionResolved:
				waiting = false
			case stream.EventDone:
				r.finish(ctx, j, msgID, text.String())
				return text.String()
			case stream.EventError:
				msg := "unknown error"
				if ev.Err != nil && ev.Err.Message != "" {
					msg = ev.Err.Message
				}
				r.edit(ctx, j.chatID, msgID, failureText(msg))
				return text.String()
			}
		}
	}
}

// finish replaces the placeholder with the first chunk of the reply
// and sends the rest as new messages.
func (r *telegramRunner) finish(ctx context.Context, j turnJob, msgID int64, text string) {
	if strings.TrimSpace(text) == "" {
		text = msgNoReply
	}
	chunks := chunkReply(text, messageLimit)
	r.edit(ctx, j.chatID, msgID, chunks[0])
	for _, c := range chunks[1:] {
		if _, err := r.bot.sendMessage(ctx, j.chatID, j.threadID, c, false); err != nil {
			if ctx.Err() == nil {
				r.svc.log.Warn("channels: send chunk failed", "channel_id", r.ch.ID, "conversation_id", j.conv.ID, "error", err)
			}
			return
		}
	}
}

func (r *telegramRunner) edit(ctx context.Context, chatID, msgID int64, text string) {
	if err := r.bot.editMessageText(ctx, chatID, msgID, text); err != nil && ctx.Err() == nil {
		r.svc.log.Warn("channels: edit failed", "channel_id", r.ch.ID, "error", err)
	}
}

func failureText(msg string) string {
	if utf8.RuneCountInString(msg) > failureCap {
		msg = string([]rune(msg)[:failureCap])
	}
	return "Something went wrong: " + msg
}
