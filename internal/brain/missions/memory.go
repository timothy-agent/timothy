package missions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/SumonMSelim/timothy/internal/brain/events"
)

// memoryExtractedKind marks that a mission's one terminal extraction
// pass has already fired — the append-only idempotency record
// ExtractMemory checks before running, since mission_events has
// no other way to record "this already happened" (D-idempotent-event,
// same pattern mission.review_skipped/mission.turn already use for
// non-transition bookkeeping).
const memoryExtractedKind = "mission.memory_extracted"

// finalOutputDigestCap bounds how much of a light mission's
// FinalOutput OutcomeDigest carries — the digest feeds memory
// extraction and a follow-up mission's parent_context, not the whole
// deliverable verbatim.
const finalOutputDigestCap = 2000

// truncateRunes is truncate's rune-safe counterpart: FinalOutput is
// user-authored model output that can contain multi-byte UTF-8, where
// byte-slicing risks cutting mid-character.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// MemoryExtract posts one mission's curated digest to memoryd for
// long-term memory extraction — the same signature and fire-and-forget
// contract as chat.MemoryExtract (see chat.go), so cmd/brain/main.go
// wires both through the identical mc.Extract call. seq is always 0: a
// mission's extraction is not tied to any session_events sequence
// number the way a chat turn's is, and memoryd's source_seq is opaque
// bookkeeping it never dereferences into the missions log.
type MemoryExtract func(ctx context.Context, sessionID string, seq int64, text string, route string)

// alreadyExtracted reports whether events already carries a
// mission.memory_extracted record — checked before every extraction
// attempt so a re-drive (sweep.go's boot-time recovery, or a Signal
// racing an Advance) can never fire the pass twice for the same
// mission.
func alreadyExtracted(events []Event) bool {
	for _, ev := range events {
		if ev.Kind == memoryExtractedKind {
			return true
		}
	}
	return false
}

// ExtractMemory runs the mission's one terminal extraction pass (D-117:
// called by MemoryConsumer for every mission.done/mission.failed
// event). Nil memory client, or a mission with no hidden session,
// skips silently. The mission.memory_extracted record makes a repeated
// call a no-op. Errors are the load/record failures a retry can fix;
// the extraction call itself stays fire-and-forget (see MemoryExtract).
func (d *Driver) ExtractMemory(ctx context.Context, id string, terminal Phase, failureReason string) error {
	if d.memory == nil {
		return nil
	}
	m, err := d.store.Get(ctx, id)
	if errors.Is(err, ErrNotFound) {
		// A deleted mission has nothing to extract; retrying cannot change that.
		d.log.Info("memory extraction: mission deleted, skipping", "mission_id", id)
		return nil
	}
	if err != nil {
		return fmt.Errorf("memory extraction: load mission: %w", err)
	}
	if m.SessionID == "" {
		return nil
	}
	events, err := d.store.Events(ctx, m.ID)
	if err != nil {
		return fmt.Errorf("memory extraction: load events: %w", err)
	}
	if alreadyExtracted(events) {
		return nil
	}
	digest := OutcomeDigest(m, events, terminal, failureReason)
	// The idempotency record must land BEFORE dispatch, not after: the
	// extraction call itself is fire-and-forget (no completion signal
	// this function can wait on), so appending after would leave the
	// exact race window this guard exists to close — a re-drive landing
	// between dispatch and a not-yet-appended event would fire a second
	// pass. Marking the attempt (not "succeeded") is the same commitment
	// AppendEvent's other bookkeeping-only callers make.
	if err := d.store.AppendEvent(ctx, m.ID, memoryExtractedKind, map[string]any{"terminal": string(terminal)}); err != nil {
		return fmt.Errorf("memory extraction: record event: %w", err)
	}
	go d.memory(context.Background(), m.SessionID, 0, digest, "") //nolint:gosec // G118: deliberate — the mission is already terminal, extraction must outlive whatever request/ctx observed that transition
	return nil
}

// MemoryConsumer is the events consumer that runs ExtractMemory for
// every terminal mission (D-117).
type MemoryConsumer struct{ d *Driver }

func NewMemoryConsumer(d *Driver) MemoryConsumer { return MemoryConsumer{d: d} }

func (MemoryConsumer) Name() string { return "mission-memory" }

func (MemoryConsumer) Kinds() []string {
	return []string{events.KindMissionDone, events.KindMissionFailed}
}

func (c MemoryConsumer) Handle(ctx context.Context, _ pgx.Tx, ev events.Event) error {
	p, err := events.DecodeMission(ev)
	if err != nil {
		return err
	}
	return c.d.ExtractMemory(ctx, p.MissionID, Phase(p.Phase), p.Reason)
}

// backfillMissionName regenerates a missing display name when a
// mission reaches the result phase (D-086): the create-time naming
// call is best-effort and a failure there would otherwise be
// permanent. Best-effort itself: never fails the step (any error is
// logged and swallowed). Runs synchronously: runResult reloads the
// mission right after this returns, and that reload is what
// destinations/kb promotion see, so the name must already be
// persisted by the time this call returns. SetNameIfEmpty's guard
// makes a late create-time call racing this one harmless.
func (d *Driver) backfillMissionName(ctx context.Context, id, goal string) {
	if d.nameMission == nil {
		return
	}
	name := d.nameMission(ctx, goal)
	if name == "" {
		d.log.Warn("mission: name backfill returned empty", "mission_id", id)
		return
	}
	if err := d.store.SetNameIfEmpty(ctx, id, name); err != nil {
		d.log.Warn("mission: name backfill save failed", "mission_id", id, "error", err)
	}
}

// OutcomeDigest assembles the curated extraction input: goal, title,
// kind, discover notes, per-unit outcomes (title/status/verify evidence
// summary only, never shell output), the review verdict (or
// review_skipped), and the terminal state/failure reason. Deliberately
// excludes anything resembling the raw transcript — build-log noise
// (tool_execution payloads, mission.turn timings) never appears here.
// Serves both memory extraction (above) and a follow-up mission's
// parent context (api/missions.go's create).
func OutcomeDigest(m Mission, events []Event, terminal Phase, failureReason string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "mission goal: %s\n", m.Goal)
	if m.Name != "" {
		fmt.Fprintf(&b, "mission title: %s\n", m.Name)
	}
	fmt.Fprintf(&b, "mission kind: %s\n", m.Kind)
	if m.DiscoverNotes != "" {
		b.WriteString("\ndiscover notes:\n")
		b.WriteString(m.DiscoverNotes)
		b.WriteString("\n")
	}
	if len(m.Plan.Units) > 0 {
		b.WriteString("\nunits:\n")
		for _, u := range m.Plan.Units {
			status := "not verified"
			if u.Passes {
				status = "passed"
			}
			fmt.Fprintf(&b, "- %s: %s\n", u.Title, status)
		}
	}
	if m.RunsPlanless() && m.FinalOutput != "" {
		b.WriteString("\nfinal output:\n")
		b.WriteString(truncateRunes(m.FinalOutput, finalOutputDigestCap))
		b.WriteString("\n")
	}
	if decision, findings, ok := lastReviewVerdict(events); ok {
		fmt.Fprintf(&b, "\nreview verdict: %s\n", decision)
		if findings != "" {
			fmt.Fprintf(&b, "review findings: %s\n", findings)
		}
	} else if reviewSkipped(events) {
		b.WriteString("\nreview: skipped (harness checks alone established the unit)\n")
	}
	fmt.Fprintf(&b, "\nterminal state: %s\n", terminal)
	if terminal == PhaseFailed && failureReason != "" {
		fmt.Fprintf(&b, "failure reason: %s\n", failureReason)
	}
	return b.String()
}

// lastReviewVerdict scans events for the most recent
// mission.review_verdict, returning its decision/findings. ok=false
// when review never ran this mission.
func lastReviewVerdict(events []Event) (decision, findings string, ok bool) {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind != "mission.review_verdict" {
			continue
		}
		var payload struct {
			Decision string `json:"decision"`
			Findings string `json:"findings"`
		}
		if err := json.Unmarshal(events[i].Payload, &payload); err != nil {
			return "", "", false
		}
		return payload.Decision, payload.Findings, true
	}
	return "", "", false
}

// reviewSkipped reports whether any mission.review_skipped event fired
// — the non-coding fast path that bypasses LLM review entirely on
// harness evidence (driver.go's routeVerified).
func reviewSkipped(events []Event) bool {
	for _, ev := range events {
		if ev.Kind == "mission.review_skipped" {
			return true
		}
	}
	return false
}
