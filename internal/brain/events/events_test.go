package events

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type fakeConsumer struct {
	name  string
	kinds []string
	fn    func(ev Event) error
}

func (f fakeConsumer) Name() string    { return f.name }
func (f fakeConsumer) Kinds() []string { return f.kinds }
func (f fakeConsumer) Handle(ctx context.Context, tx pgx.Tx, ev Event) error {
	if f.fn == nil {
		return nil
	}
	return f.fn(ev)
}

func TestConsumersFor(t *testing.T) {
	a := fakeConsumer{name: "a", kinds: []string{KindMissionDone, KindMissionFailed}}
	b := fakeConsumer{name: "b", kinds: []string{KindMissionDone}}
	c := fakeConsumer{name: "c", kinds: nil}
	all := []Consumer{a, b, c}
	tests := []struct {
		kind string
		want []string
	}{
		{KindMissionDone, []string{"a", "b"}},
		{KindMissionFailed, []string{"a"}},
		{"cron.due", nil},
		{"", nil},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			var got []string
			for _, c := range consumersFor(all, tt.kind) {
				got = append(got, c.Name())
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("consumersFor(%q) = %v, want %v", tt.kind, got, tt.want)
			}
		})
	}
}

func TestOutcome(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name          string
		attempts      int
		err           error
		wantProcessed bool
		wantDead      bool
	}{
		{"first success", 0, nil, true, false},
		{"success after failures", 3, nil, true, false},
		{"first failure retries", 0, boom, false, false},
		{"fourth failure retries", 3, boom, false, false},
		{"fifth failure dead-letters", 4, boom, true, true},
		{"past cap dead-letters", 7, boom, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			processed, dead := outcome(tt.attempts, tt.err)
			if processed != tt.wantProcessed || dead != tt.wantDead {
				t.Fatalf("outcome(%d, %v) = (%v, %v), want (%v, %v)", tt.attempts, tt.err, processed, dead, tt.wantProcessed, tt.wantDead)
			}
		})
	}
}

func TestResultLabel(t *testing.T) {
	tests := []struct {
		processed, dead bool
		want            string
	}{
		{true, false, "ok"},
		{false, false, "retry"},
		{true, true, "dead_letter"},
	}
	for _, tt := range tests {
		if got := resultLabel(tt.processed, tt.dead); got != tt.want {
			t.Fatalf("resultLabel(%v, %v) = %q, want %q", tt.processed, tt.dead, got, tt.want)
		}
	}
}

func TestMissionTerminal(t *testing.T) {
	tests := []struct {
		name     string
		in       MissionPayload
		wantKind string
		wantJSON string
		wantErr  bool
	}{
		{
			name:     "done omits empty strings",
			in:       MissionPayload{MissionID: "m1", Phase: "done", OriginKind: "api"},
			wantKind: KindMissionDone,
			wantJSON: `{"mission_id":"m1","phase":"done","origin_kind":"api","unattended":false}`,
		},
		{
			name:     "failed carries reason and workflow run",
			in:       MissionPayload{MissionID: "m2", Phase: "failed", Reason: "cap", WorkflowRunID: "r1", OriginKind: "workflow", Unattended: true},
			wantKind: KindMissionFailed,
			wantJSON: `{"mission_id":"m2","phase":"failed","reason":"cap","workflow_run_id":"r1","origin_kind":"workflow","unattended":true}`,
		},
		{name: "non-terminal phase rejected", in: MissionPayload{MissionID: "m3", Phase: "build"}, wantErr: true},
		{name: "missing mission id rejected", in: MissionPayload{Phase: "done"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev, err := MissionTerminal(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("MissionTerminal(%+v) = nil error, want error", tt.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("MissionTerminal: %v", err)
			}
			if ev.Source != SourceMission || ev.Kind != tt.wantKind || ev.DedupKey != tt.in.MissionID {
				t.Fatalf("event = %+v", ev)
			}
			if string(ev.Payload) != tt.wantJSON {
				t.Fatalf("payload = %s, want %s", ev.Payload, tt.wantJSON)
			}
			back, err := DecodeMission(ev)
			if err != nil || back != tt.in {
				t.Fatalf("DecodeMission = %+v, %v, want %+v", back, err, tt.in)
			}
		})
	}
}

func TestDecodeMissionRejectsBadPayload(t *testing.T) {
	for _, raw := range []string{`not json`, `{}`, `{"phase":"done"}`} {
		if _, err := DecodeMission(Event{ID: 1, Payload: json.RawMessage(raw)}); err == nil {
			t.Fatalf("DecodeMission(%s) = nil error, want error", raw)
		}
	}
}

func TestHandleSafely(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name    string
		fn      func(ev Event) error
		wantErr string
	}{
		{"success", func(Event) error { return nil }, ""},
		{"error passes through", func(Event) error { return boom }, "boom"},
		{"panic becomes error", func(Event) error { panic("kaboom") }, "panic: kaboom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := handleSafely(context.Background(), fakeConsumer{name: "x", fn: tt.fn}, nil, Event{ID: 1})
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("handleSafely = %v, want nil", err)
				}
				return
			}
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("handleSafely = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestHandleSafelyAppliesTimeout(t *testing.T) {
	var deadlineSet bool
	c := ctxConsumer{fn: func(ctx context.Context) { _, deadlineSet = ctx.Deadline() }}
	if err := handleSafely(context.Background(), c, nil, Event{}); err != nil {
		t.Fatalf("handleSafely: %v", err)
	}
	if !deadlineSet {
		t.Fatal("consumer ctx has no deadline, want consumerTimeout applied")
	}
}

type ctxConsumer struct{ fn func(ctx context.Context) }

func (ctxConsumer) Name() string    { return "ctx" }
func (ctxConsumer) Kinds() []string { return nil }
func (c ctxConsumer) Handle(ctx context.Context, tx pgx.Tx, ev Event) error {
	c.fn(ctx)
	return nil
}

func TestKickNeverBlocks(t *testing.T) {
	d := NewDrainer(nil, nil, nil, nil)
	for range 3 {
		d.Kick()
	}
	if len(d.kick) != 1 {
		t.Fatalf("pending kicks = %d, want 1", len(d.kick))
	}
}

func TestConnectorEvent(t *testing.T) {
	base := ConnectorEventPayload{Provider: "github", ConnectorID: "c1", Repo: "o/r", Kind: KindPROpened, ProviderEventID: "42"}
	ev, err := ConnectorEvent(base)
	if err != nil {
		t.Fatalf("ConnectorEvent: %v", err)
	}
	if ev.Source != SourceConnector || ev.Kind != KindPROpened || ev.DedupKey != "github:c1:42" {
		t.Fatalf("event = %+v", ev)
	}
	got, err := DecodeConnectorEvent(ev)
	if err != nil || got.Repo != "o/r" || got.ProviderEventID != "42" {
		t.Fatalf("DecodeConnectorEvent = %+v, %v", got, err)
	}

	long := base
	long.Body = strings.Repeat("é", MaxConnectorBodyBytes)
	ev, err = ConnectorEvent(long)
	if err != nil {
		t.Fatalf("ConnectorEvent long: %v", err)
	}
	got, _ = DecodeConnectorEvent(ev)
	if len(got.Body) > MaxConnectorBodyBytes || len(got.Body) < MaxConnectorBodyBytes-1 || !utf8.ValidString(got.Body) {
		t.Fatalf("body = %d bytes, valid %v; want capped at %d on a rune boundary", len(got.Body), utf8.ValidString(got.Body), MaxConnectorBodyBytes)
	}

	for name, p := range map[string]ConnectorEventPayload{
		"unknown kind":   {Provider: "github", ConnectorID: "c1", Kind: "pr.closed", ProviderEventID: "1"},
		"no connector":   {Provider: "github", Kind: KindPROpened, ProviderEventID: "1"},
		"no provider id": {Provider: "github", ConnectorID: "c1", Kind: KindPROpened},
		"no provider":    {ConnectorID: "c1", Kind: KindPROpened, ProviderEventID: "1"},
	} {
		if _, err := ConnectorEvent(p); err == nil {
			t.Errorf("%s: ConnectorEvent accepted %+v", name, p)
		}
	}
	if _, err := DecodeConnectorEvent(Event{ID: 1, Payload: json.RawMessage(`{"kind":"pr.opened"}`)}); err == nil {
		t.Error("DecodeConnectorEvent accepted a payload without ids")
	}
}
