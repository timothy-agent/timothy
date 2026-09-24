package channels

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

// park is one push for a mission waiting on the operator.
type park struct {
	// Key identifies the park; a mission is pushed once per key.
	Key      string
	Text     string
	Keyboard [][]button
	// AskKind, when set, remembers the message so a reply answers it.
	AskKind string
}

// renderPark returns the push for a mission's current park; ok is
// false when it waits on nothing.
func renderPark(m missions.Mission) (park, bool) {
	if m.Phase.Terminal() {
		return park{}, false
	}
	title := missions.PRTitle(m)
	switch {
	case m.PendingPermission != "":
		return park{
			Key:      "perm:" + m.PendingPermission,
			Text:     permText("Mission "+title, m.PendingPermissionTool, m.PendingPermissionRationale),
			Keyboard: permKeyboard(m.PendingPermission),
		}, true
	case m.PendingInput != nil:
		in := m.PendingInput
		p := park{Key: "ask:" + in.AskedAt.UTC().Format(time.RFC3339Nano)}
		text := "Mission " + title + " asks: " + in.Question
		if in.ProposedDefault != "" {
			text += "\nProposed: " + in.ProposedDefault
		}
		for i, o := range in.Options {
			if i == maxAskButtons {
				break
			}
			p.Keyboard = append(p.Keyboard, []button{{Text: capRunes(o, 60), Data: askCallback(m.ID, i)}})
		}
		if len(p.Keyboard) == 0 {
			text += "\n\n" + msgReplyHint
			p.AskKind = AskUser
		}
		p.Text = text
		return p, true
	case m.Status == missions.StatusPaused && m.Phase == missions.PhasePlan && m.PauseReason == missions.PauseApproval:
		return park{
			Key:  "plan:" + m.UpdatedAt.UTC().Format(time.RFC3339Nano),
			Text: "Mission " + title + " has a plan ready\n\n" + planSummary(m.Plan) + "\n\n" + msgReplanHint,
			Keyboard: [][]button{{
				{Text: "Approve", Data: missionCallback(m.ID, actPlanApprove)},
				{Text: "Replan", Data: missionCallback(m.ID, actPlanReplan)},
			}},
			AskKind: AskPlan,
		}, true
	case m.Status == missions.StatusPaused:
		reason := m.PauseMessage
		if reason == "" {
			reason = string(m.PauseReason)
		}
		return park{
			Key:  "paused:" + m.UpdatedAt.UTC().Format(time.RFC3339Nano),
			Text: "Mission " + title + " paused: " + capRunes(reason, failureCap),
			Keyboard: [][]button{{
				{Text: "Resume", Data: missionCallback(m.ID, actResume)},
				{Text: "Cancel", Data: missionCallback(m.ID, actCancel)},
			}},
		}, true
	}
	return park{}, false
}

// planSummary lists the plan's units, capped at digestCap.
func planSummary(p missions.Plan) string {
	if len(p.Units) == 0 {
		return "(no units)"
	}
	var b strings.Builder
	for i, u := range p.Units {
		fmt.Fprintf(&b, "%d. %s\n", i+1, u.Title)
	}
	return capRunes(strings.TrimRight(b.String(), "\n"), digestCap)
}

// watchParks pushes parks of channel missions on hub signals and on a
// periodic reconcile until ctx ends.
func (s *Service) watchParks(ctx context.Context) {
	var signals <-chan missions.Signal
	if s.missions.Subscribe != nil {
		signals = s.missions.Subscribe(ctx)
	}
	t := time.NewTicker(s.parkEvery)
	defer t.Stop()
	s.reconcileParks(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case sig, ok := <-signals:
			if !ok {
				signals = nil
				continue
			}
			if sig.Kind != "mission" && sig.Kind != "permission" {
				continue
			}
			// A permission signal for a chat prompt carries a session id;
			// the lookup fails and is ignored.
			m, err := s.missions.Get(ctx, sig.ID)
			if err != nil || m.ChannelConversationID == "" {
				continue
			}
			s.pushPark(ctx, m)
		case <-t.C:
			s.reconcileParks(ctx)
		}
	}
}

// reconcileParks pushes every parked channel mission a signal missed.
func (s *Service) reconcileParks(ctx context.Context) {
	if s.missions.ListParked == nil {
		return
	}
	list, err := s.missions.ListParked(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Warn("channels: list parked missions failed", "error", err)
		}
		return
	}
	for _, m := range list {
		s.pushPark(ctx, m)
	}
}

// pushPark sends a mission's park to its conversation once per key.
// Keys live in memory, so a restart may repeat one message per parked
// mission.
func (s *Service) pushPark(ctx context.Context, m missions.Mission) {
	p, ok := renderPark(m)
	if !ok {
		delete(s.parked, m.ID)
		delete(s.skipLogged, m.ID)
		return
	}
	if s.parked[m.ID] == p.Key {
		return
	}
	conv, err := s.store.ConversationByID(ctx, m.ChannelConversationID)
	if err != nil {
		s.skipOnce(m.ID, "conversation not found", err)
		return
	}
	ch, err := s.store.Get(ctx, conv.ChannelID)
	if err != nil {
		s.skipOnce(m.ID, "channel not found", err)
		return
	}
	if !ch.Enabled || (s.enabled != nil && !s.enabled(ctx)) {
		s.skipOnce(m.ID, "channel disabled", nil)
		return
	}
	ad, err := s.adapterFor(ch)
	if err != nil {
		s.skipOnce(m.ID, "unsupported channel", err)
		return
	}
	msgID, err := ad.send(ctx, conversationTarget(conv), p.Text, p.Keyboard, false)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Warn("channels: push park failed", "channel_id", ch.ID, "mission_id", m.ID, "error", err)
		}
		return
	}
	s.parked[m.ID] = p.Key
	delete(s.skipLogged, m.ID)
	if p.AskKind != "" {
		if err := s.store.RememberAsk(ctx, conv.ID, msgID, m.ID, p.AskKind); err != nil {
			s.log.Warn("channels: remember ask failed", "conversation_id", conv.ID, "mission_id", m.ID, "error", err)
		}
	}
	s.log.Info("channels: pushed park", "channel_id", ch.ID, "conversation_id", conv.ID, "mission_id", m.ID, "park", strings.SplitN(p.Key, ":", 2)[0])
}

// skipOnce logs a skipped push once per mission.
func (s *Service) skipOnce(missionID, why string, err error) {
	if s.skipLogged[missionID] {
		return
	}
	s.skipLogged[missionID] = true
	if err != nil {
		s.log.Info("channels: park not pushed", "mission_id", missionID, "reason", why, "error", err)
		return
	}
	s.log.Info("channels: park not pushed", "mission_id", missionID, "reason", why)
}
