package workflows

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/SumonMSelim/timothy/internal/brain/events"
)

// Name, Kinds and Handle make Engine the events consumer that advances
// a workflow run when one of its step missions ends (D-117).
func (e *Engine) Name() string { return "workflows" }

func (e *Engine) Kinds() []string {
	return []string{events.KindMissionDone, events.KindMissionFailed}
}

// Handle loads the event's mission and runs OnMissionTerminal; a
// mission outside any workflow run is a no-op.
func (e *Engine) Handle(ctx context.Context, _ pgx.Tx, ev events.Event) error {
	p, err := events.DecodeMission(ev)
	if err != nil {
		return err
	}
	if p.WorkflowRunID == "" {
		return nil
	}
	m, err := e.events.Get(ctx, p.MissionID)
	if err != nil {
		return fmt.Errorf("workflows: load mission %s: %w", p.MissionID, err)
	}
	return e.OnMissionTerminal(ctx, m)
}

// edgeTakenFor reports whether the run already took an edge for
// missionID, which makes a redelivered terminal event a no-op.
func edgeTakenFor(runEvents []RunEvent, missionID string) bool {
	for _, ev := range runEvents {
		if ev.Kind != "edge.taken" {
			continue
		}
		var p struct {
			MissionID string `json:"mission_id"`
		}
		if json.Unmarshal(ev.Payload, &p) == nil && p.MissionID == missionID {
			return true
		}
	}
	return false
}
