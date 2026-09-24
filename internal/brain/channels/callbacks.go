package channels

import (
	"context"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/SumonMSelim/timothy/internal/brain/loop"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

// Button and callback copy.
const (
	msgNotPaired       = "Not paired"
	msgNotYours        = "Not yours"
	msgAlreadyAnswered = "Already answered"
	msgUnknownButton   = "Unknown button"
	msgUnavailable     = "Not available"
	msgSentToMission   = "Sent to the mission."
	msgReplyHint       = "Reply to this message with your answer."
	msgReplanHint      = "Reply to this message to add feedback and replan."
	answerCap          = 200
)

// Callback kinds: a permission prompt or a mission action.
const (
	cbPermission = "p"
	cbMission    = "m"
)

// Mission button actions.
const (
	actPlanApprove = "plan_approve"
	actPlanReplan  = "plan_replan"
	actResume      = "resume"
	actCancel      = "cancel"
	actAsk         = "ask"
)

// callback is one decoded callback_data: p:<id>:<decision> or
// m:<mission id>:<action>[:<option index>].
type callback struct {
	Kind   string
	ID     string
	Action string
	Index  int
}

func permCallback(id, decision string) string { return cbPermission + ":" + id + ":" + decision }

func missionCallback(id, action string) string { return cbMission + ":" + id + ":" + action }

func askCallback(id string, i int) string { return missionCallback(id, actAsk+":"+strconv.Itoa(i)) }

// validCallbackID accepts permission ids (hex) and mission ids (uuid).
func validCallbackID(id string) bool {
	if id == "" || len(id) > 40 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F', r == '-':
		default:
			return false
		}
	}
	return true
}

// parseCallback decodes callback_data; ok is false for anything this
// bot did not send.
func parseCallback(data string) (callback, bool) {
	if len(data) > callbackDataLimit {
		return callback{}, false
	}
	parts := strings.Split(data, ":")
	if len(parts) < 3 || !validCallbackID(parts[1]) {
		return callback{}, false
	}
	cb := callback{Kind: parts[0], ID: parts[1], Action: parts[2]}
	switch cb.Kind {
	case cbPermission:
		if len(parts) != 3 {
			return callback{}, false
		}
		switch cb.Action {
		case loop.DecideOnce, loop.DecideSession, loop.DecideDeny:
			return cb, true
		}
	case cbMission:
		switch cb.Action {
		case actPlanApprove, actPlanReplan, actResume, actCancel:
			return cb, len(parts) == 3
		case actAsk:
			if len(parts) != 4 {
				return callback{}, false
			}
			i, err := strconv.Atoi(parts[3])
			if err != nil || i < 0 || i >= maxAskButtons || parts[3] != strconv.Itoa(i) {
				return callback{}, false
			}
			cb.Index = i
			return cb, true
		}
	}
	return callback{}, false
}

// permKeyboard is the three decision buttons of a permission prompt.
func permKeyboard(id string) [][]button {
	return [][]button{{
		{Text: "Allow once", Data: permCallback(id, loop.DecideOnce)},
		{Text: "Allow session", Data: permCallback(id, loop.DecideSession)},
		{Text: "Deny", Data: permCallback(id, loop.DecideDeny)},
	}}
}

// permText is a permission prompt's message text.
func permText(who, tool, rationale string) string {
	text := who + " wants to run " + tool
	if r := strings.TrimSpace(rationale); r != "" {
		text += ": " + capRunes(r, rationaleCap)
	}
	return text
}

// decisionText is what a resolved prompt's message reads.
func decisionText(decision string) string {
	switch decision {
	case loop.DecideOnce:
		return "Allowed once"
	case loop.DecideSession:
		return "Allowed for this session"
	case loop.DecideDeny:
		return "Denied"
	default:
		return "Resolved"
	}
}

func capRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// authorizeCallback decides whether a press may act on a target owned
// by ownerSession or ownerConv: the presser must be an approved sender
// and the chat must be the owning conversation. "" allows; otherwise
// the answer to show.
func authorizeCallback(p Pairing, conv Conversation, found bool, ownerSession, ownerConv string) string {
	if p.Status != StatusApproved {
		return msgNotPaired
	}
	if !found {
		return msgNotYours
	}
	if (ownerSession != "" && ownerSession == conv.SessionID) || (ownerConv != "" && ownerConv == conv.ID) {
		return ""
	}
	return msgNotYours
}

// handlePress runs one button press through pairing, conversation
// ownership and dispatch. Every press is answered once. Only store
// failures return an error.
func (r *runner) handlePress(ctx context.Context, in inbound) error {
	q := in.Press
	answer := func(text string) {
		if err := r.ad.answerPress(ctx, q.ID, capRunes(text, answerCap)); err != nil && ctx.Err() == nil {
			r.svc.log.Warn("channels: answer callback failed", "channel_id", r.ch.ID, "error", err)
		}
	}
	if in.UserID == "" {
		answer("")
		return nil
	}
	cb, ok := parseCallback(q.Data)
	if !ok {
		answer(msgUnknownButton)
		return nil
	}
	now := r.svc.now()
	p, _, err := r.svc.store.EnsurePairing(ctx, r.ch.ID, in.UserID, in.DisplayName, now)
	if err != nil {
		return err
	}
	if p.Status != StatusApproved {
		answer(msgNotPaired)
		return nil
	}
	if ok, _ := r.limits.allow(r.ch.ID+":"+in.UserID, now); !ok {
		answer(msgSlowDown)
		return nil
	}
	key := conversationKey(in)
	conv, found, err := r.svc.store.ConversationFor(ctx, r.ch.ID, key.ChatID, key.ThreadID)
	if err != nil {
		return err
	}
	if !found {
		answer(msgNotYours)
		return nil
	}
	if cb.Kind == cbPermission {
		r.pressPermission(ctx, in, cb, p, conv, answer)
	} else {
		r.pressMission(ctx, in, cb, p, conv, answer)
	}
	return nil
}

func (r *runner) pressPermission(ctx context.Context, in inbound, cb callback, p Pairing, conv Conversation, answer func(string)) {
	deps := r.svc.missions
	if deps.PendingPermission == nil || deps.ResolvePermission == nil {
		answer(msgUnavailable)
		return
	}
	perm, ok, err := deps.PendingPermission(ctx, cb.ID)
	if err != nil {
		r.svc.log.Warn("channels: load permission failed", "channel_id", r.ch.ID, "error", err)
		answer("Something went wrong, try again")
		return
	}
	if !ok {
		answer(msgAlreadyAnswered)
		return
	}
	var ownerConv string
	if perm.MissionID != "" && deps.Get != nil {
		if m, err := deps.Get(ctx, perm.MissionID); err == nil {
			ownerConv = m.ChannelConversationID
		}
	}
	if v := authorizeCallback(p, conv, true, perm.SessionID, ownerConv); v != "" {
		r.svc.log.Warn("channels: button for another conversation", "channel_id", r.ch.ID, "conversation_id", conv.ID, "kind", cb.Kind)
		answer(v)
		return
	}
	if !deps.ResolvePermission(ctx, cb.ID, cb.Action) {
		answer(msgAlreadyAnswered)
		return
	}
	text := decisionText(cb.Action)
	answer(text)
	if err := r.ad.edit(ctx, conversationKey(in), in.Press.MessageID, text, nil); err != nil && ctx.Err() == nil {
		r.svc.log.Warn("channels: close buttons failed", "channel_id", r.ch.ID, "error", err)
	}
}

func (r *runner) pressMission(ctx context.Context, in inbound, cb callback, p Pairing, conv Conversation, answer func(string)) {
	deps := r.svc.missions
	if deps.Get == nil || deps.Signal == nil || deps.DecidePlan == nil || deps.AnswerAskUser == nil {
		answer(msgUnavailable)
		return
	}
	m, err := deps.Get(ctx, cb.ID)
	if err != nil {
		answer("Mission not found")
		return
	}
	if v := authorizeCallback(p, conv, true, "", m.ChannelConversationID); v != "" {
		r.svc.log.Warn("channels: button for another conversation", "channel_id", r.ch.ID, "conversation_id", conv.ID, "kind", cb.Kind)
		answer(v)
		return
	}
	var done string
	switch cb.Action {
	case actPlanApprove:
		err, done = deps.DecidePlan(ctx, m.ID, missions.InputPlanApprove, ""), "Plan approved"
	case actPlanReplan:
		err, done = deps.DecidePlan(ctx, m.ID, missions.InputPlanReplan, ""), "Replanning"
	case actResume:
		err, done = deps.Signal(ctx, m.ID, missions.InputResume), "Resumed"
	case actCancel:
		err, done = deps.Signal(ctx, m.ID, missions.InputCancel), "Cancelled"
	case actAsk:
		if m.PendingInput == nil || cb.Index >= len(m.PendingInput.Options) {
			answer(msgAlreadyAnswered)
			return
		}
		option := m.PendingInput.Options[cb.Index]
		err, done = deps.AnswerAskUser(ctx, m.ID, option), "Answered: "+option
	}
	if err != nil {
		answer(err.Error())
		return
	}
	answer(done)
	text := done
	if in.Press.MessageText != "" {
		text = in.Press.MessageText + "\n\n" + done
	}
	if err := r.ad.edit(ctx, conversationKey(in), in.Press.MessageID, capRunes(text, r.ad.caps().MessageLimit/2), nil); err != nil && ctx.Err() == nil {
		r.svc.log.Warn("channels: close buttons failed", "channel_id", r.ch.ID, "error", err)
	}
}

// answerAsk sends a reply-to answer to the mission that asked.
func (r *runner) answerAsk(ctx context.Context, in inbound, missionID, kind string) {
	deps := r.svc.missions
	var err error
	switch {
	case kind == AskUser && deps.AnswerAskUser != nil:
		err = deps.AnswerAskUser(ctx, missionID, in.Text)
	case kind == AskPlan && deps.DecidePlan != nil:
		err = deps.DecidePlan(ctx, missionID, missions.InputPlanReplan, in.Text)
	default:
		r.reply(ctx, in, msgUnavailable)
		return
	}
	if err != nil {
		r.reply(ctx, in, "Could not send: "+capRunes(err.Error(), failureCap))
		return
	}
	r.reply(ctx, in, msgSentToMission)
}
