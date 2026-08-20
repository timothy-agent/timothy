package destinations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type fakeConns struct {
	rows map[string]Connector
}

func (f fakeConns) Get(_ context.Context, id string) (Connector, error) {
	c, ok := f.rows[id]
	if !ok {
		return Connector{}, errors.New("connector not found")
	}
	return c, nil
}

func TestValidate(t *testing.T) {
	conns := fakeConns{rows: map[string]Connector{
		"gmail-ok":       {Kind: "google", Enabled: true},
		"gmail-disabled": {Kind: "google", Enabled: false},
		"mcp-conn":       {Kind: "mcp", Enabled: true},
	}}

	tests := []struct {
		name    string
		d       Destination
		wantErr bool
	}{
		{
			name: "valid email",
			d: Destination{Name: "ops-inbox", Kind: "email",
				Config: json.RawMessage(`{"connector_id":"gmail-ok","to":"ops@example.com"}`)},
			wantErr: false,
		},
		{
			name: "email missing connector_id",
			d: Destination{Name: "ops-inbox", Kind: "email",
				Config: json.RawMessage(`{"to":"ops@example.com"}`)},
			wantErr: true,
		},
		{
			name: "email missing to",
			d: Destination{Name: "ops-inbox", Kind: "email",
				Config: json.RawMessage(`{"connector_id":"gmail-ok"}`)},
			wantErr: true,
		},
		{
			name: "email connector not google-kind",
			d: Destination{Name: "ops-inbox", Kind: "email",
				Config: json.RawMessage(`{"connector_id":"mcp-conn","to":"ops@example.com"}`)},
			wantErr: true,
		},
		{
			name: "email connector disabled",
			d: Destination{Name: "ops-inbox", Kind: "email",
				Config: json.RawMessage(`{"connector_id":"gmail-disabled","to":"ops@example.com"}`)},
			wantErr: true,
		},
		{
			name: "email connector unknown",
			d: Destination{Name: "ops-inbox", Kind: "email",
				Config: json.RawMessage(`{"connector_id":"nope","to":"ops@example.com"}`)},
			wantErr: true,
		},
		{
			name: "valid webhook json",
			d: Destination{Name: "ops-hook", Kind: "webhook",
				Config: json.RawMessage(`{"url":"https://example.com/hook","format":"json"}`)},
			wantErr: false,
		},
		{
			name: "valid webhook text",
			d: Destination{Name: "ops-hook", Kind: "webhook",
				Config: json.RawMessage(`{"url":"http://example.com/hook","format":"text"}`)},
			wantErr: false,
		},
		{
			name: "webhook bad url scheme",
			d: Destination{Name: "ops-hook", Kind: "webhook",
				Config: json.RawMessage(`{"url":"ftp://example.com/hook","format":"json"}`)},
			wantErr: true,
		},
		{
			name: "webhook bad format",
			d: Destination{Name: "ops-hook", Kind: "webhook",
				Config: json.RawMessage(`{"url":"https://example.com/hook","format":"xml"}`)},
			wantErr: true,
		},
		{
			name:    "telegram rejected at go layer",
			d:       Destination{Name: "tg", Kind: "telegram", Config: json.RawMessage(`{"chat_id":"123"}`)},
			wantErr: true,
		},
		{
			name:    "unknown kind rejected",
			d:       Destination{Name: "whatsapp", Kind: "whatsapp", Config: json.RawMessage(`{}`)},
			wantErr: true,
		},
		{
			name:    "bad name slug",
			d:       Destination{Name: "Ops Inbox", Kind: "webhook", Config: json.RawMessage(`{"url":"https://example.com","format":"json"}`)},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validate(t.Context(), conns, tt.d)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateEmailNoConnectors(t *testing.T) {
	d := Destination{Name: "ops-inbox", Kind: "email",
		Config: json.RawMessage(`{"connector_id":"gmail-ok","to":"ops@example.com"}`)}
	if err := validate(t.Context(), nil, d); err == nil {
		t.Fatal("expected error when connectors are disabled")
	}
}

// fakeMissionRefs/fakeScheduleRefs let TestDeleteReferenceGuards
// exercise Delete's two reference checks without a real Postgres pool
// — both fakes return before Delete ever reaches s.db.Get() when they
// report a reference, which is the only path this test needs (a
// non-referenced Delete would go on to hit the db and belongs in the
// integration suite instead).
type fakeMissionRefs struct {
	referenced bool
	err        error
}

func (f fakeMissionRefs) ActiveMissionReferencesDestination(context.Context, string) (bool, error) {
	return f.referenced, f.err
}

type fakeScheduleRefs struct {
	name       string
	referenced bool
	err        error
}

func (f fakeScheduleRefs) ScheduleReferencingDestinationID(context.Context, string) (string, bool, error) {
	return f.name, f.referenced, f.err
}

func TestDeleteReferenceGuards(t *testing.T) {
	t.Parallel()

	t.Run("active mission reference refuses with ErrReferenced", func(t *testing.T) {
		t.Parallel()
		s := &Store{}
		err := s.Delete(t.Context(), "d1", fakeMissionRefs{referenced: true}, nil)
		if !errors.Is(err, ErrReferenced) {
			t.Fatalf("Delete = %v, want ErrReferenced", err)
		}
	})

	t.Run("mission reference lookup error propagates", func(t *testing.T) {
		t.Parallel()
		s := &Store{}
		err := s.Delete(t.Context(), "d1", fakeMissionRefs{err: errors.New("db down")}, nil)
		if err == nil || errors.Is(err, ErrReferenced) {
			t.Fatalf("Delete = %v, want a propagated (non-ErrReferenced) error", err)
		}
	})

	t.Run("enabled schedule reference refuses with ErrReferenced naming the schedule", func(t *testing.T) {
		t.Parallel()
		s := &Store{}
		err := s.Delete(t.Context(), "d1", nil, fakeScheduleRefs{name: "daily-brief", referenced: true})
		if !errors.Is(err, ErrReferenced) {
			t.Fatalf("Delete = %v, want ErrReferenced", err)
		}
		if !bytes.Contains([]byte(err.Error()), []byte("daily-brief")) {
			t.Fatalf("Delete error %q does not name the referencing schedule", err.Error())
		}
	})

	t.Run("schedule reference lookup error propagates", func(t *testing.T) {
		t.Parallel()
		s := &Store{}
		err := s.Delete(t.Context(), "d1", nil, fakeScheduleRefs{err: errors.New("db down")})
		if err == nil || errors.Is(err, ErrReferenced) {
			t.Fatalf("Delete = %v, want a propagated (non-ErrReferenced) error", err)
		}
	})
}
