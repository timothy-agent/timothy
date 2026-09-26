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
		"gh-ok":          {Kind: "github", Enabled: true},
		"bb-ok":          {Kind: "bitbucket", Enabled: true},
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
			name:    "valid bitbucket",
			d:       Destination{Name: "bb", Kind: "bitbucket", Config: json.RawMessage(`{"connector_id":"bb-ok","mode":"push_pr"}`)},
			wantErr: false,
		},
		{
			name:    "bitbucket with a github connector",
			d:       Destination{Name: "bb", Kind: "bitbucket", Config: json.RawMessage(`{"connector_id":"gh-ok","mode":"push"}`)},
			wantErr: true,
		},
		{
			name:    "github with a bitbucket connector",
			d:       Destination{Name: "gh", Kind: "github", Config: json.RawMessage(`{"connector_id":"bb-ok","mode":"push"}`)},
			wantErr: true,
		},
		{
			name:    "bitbucket bad mode",
			d:       Destination{Name: "bb", Kind: "bitbucket", Config: json.RawMessage(`{"connector_id":"bb-ok","mode":"merge"}`)},
			wantErr: true,
		},
		{
			name:    "bitbucket must not set credential_ref",
			d:       Destination{Name: "bb", Kind: "bitbucket", CredentialRef: "X", Config: json.RawMessage(`{"connector_id":"bb-ok","mode":"push"}`)},
			wantErr: true,
		},
		{
			name:    "unknown kind rejected",
			d:       Destination{Name: "whatsapp", Kind: "whatsapp", Config: json.RawMessage(`{}`)},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validate(t.Context(), conns, fakeChannels, &tt.d)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateEmailNoConnectors(t *testing.T) {
	d := Destination{Name: "ops-inbox", Kind: "email",
		Config: json.RawMessage(`{"connector_id":"gmail-ok","to":"ops@example.com"}`)}
	if err := validate(t.Context(), nil, nil, &d); err == nil {
		t.Fatal("expected error when connectors are disabled")
	}
}

// fakeChannels knows one channel of each kind.
func fakeChannels(_ context.Context, id string) (ChannelRef, error) {
	kinds := map[string]string{"tg": ChannelTelegram, "sl": ChannelSlack, "em": ChannelEmail, "fax": "fax"}
	kind, ok := kinds[id]
	if !ok {
		return ChannelRef{}, errors.New("channel not found")
	}
	//nolint:gosec // G101: ref names, not credential values.
	return ChannelRef{Kind: kind, CredentialRef: "BOT_REF", ConnectorID: "imap-1"}, nil
}

func TestValidateChannel(t *testing.T) {
	tests := []struct {
		name     string
		config   string
		ref      string
		channels ChannelLookup
		wantErr  bool
	}{
		{name: "telegram", config: `{"channel_id":"tg","chat_id":"-100123"}`},
		{name: "telegram topic", config: `{"channel_id":"tg","chat_id":"-100123","thread_id":"7"}`},
		{name: "telegram missing chat_id", config: `{"channel_id":"tg"}`, wantErr: true},
		{name: "slack", config: `{"channel_id":"sl","chat_id":"C0123"}`},
		{name: "slack blank chat_id", config: `{"channel_id":"sl","chat_id":"  "}`, wantErr: true},
		{name: "email", config: `{"channel_id":"em","to":"ops@example.com"}`},
		{name: "email missing to", config: `{"channel_id":"em","chat_id":"x"}`, wantErr: true},
		{name: "email two recipients", config: `{"channel_id":"em","to":"a@example.com, b@example.com"}`, wantErr: true},
		{name: "email display name", config: `{"channel_id":"em","to":"Ops <ops@example.com>"}`, wantErr: true},
		{name: "missing channel_id", config: `{"chat_id":"1"}`, wantErr: true},
		{name: "unknown channel", config: `{"channel_id":"nope","chat_id":"1"}`, wantErr: true},
		{name: "channel kind that cannot deliver", config: `{"channel_id":"fax","chat_id":"1"}`, wantErr: true},
		{name: "credential_ref rejected", config: `{"channel_id":"tg","chat_id":"1"}`, ref: "BOT_REF", wantErr: true},
		{name: "malformed config", config: `not json`, wantErr: true},
		{name: "channels disabled", config: `{"channel_id":"tg","chat_id":"1"}`, channels: nil, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookup := tt.channels
			if lookup == nil && tt.name != "channels disabled" {
				lookup = fakeChannels
			}
			d := Destination{Name: "chan", Kind: "channel", CredentialRef: tt.ref, Config: json.RawMessage(tt.config)}
			if err := validate(t.Context(), nil, lookup, &d); (err != nil) != tt.wantErr {
				t.Fatalf("validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
	//nolint:gosec // G101: a ref NAME, not a credential value.
	d := Destination{Name: "tg", Kind: "telegram", CredentialRef: "TG_BOT_TOKEN", Config: json.RawMessage(`{"chat_id":"123"}`)}
	if err := validate(t.Context(), nil, fakeChannels, &d); err == nil {
		t.Fatal("the retired telegram kind must be rejected")
	}
}

// fakeMissionRefs/fakeAutomationRefs let TestDeleteReferenceGuards
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

type fakeAutomationRefs struct {
	name       string
	referenced bool
	err        error
}

func (f fakeAutomationRefs) NameReferencingDestination(context.Context, string) (string, bool, error) {
	return f.name, f.referenced, f.err
}

// TestCreateWrapsValidationInErrInvalid covers each validate branch
// through Create: every rejection wraps ErrInvalid before the db is
// touched, so the API answers 400 rather than 500.
func TestCreateWrapsValidationInErrInvalid(t *testing.T) {
	t.Parallel()
	conns := fakeConns{rows: map[string]Connector{
		"gmail-ok": {Kind: "google", Enabled: true},
		"mcp-conn": {Kind: "mcp", Enabled: true},
	}}
	s := &Store{conns: conns, channels: fakeChannels}
	for _, tt := range []struct {
		name string
		d    Destination
	}{
		{"blank name", Destination{Name: " ", Kind: "webhook"}},
		{"email connector not google-kind", Destination{Name: "e", Kind: "email", Config: json.RawMessage(`{"connector_id":"mcp-conn","to":"a@example.com"}`)}},
		{"webhook bad url", Destination{Name: "w", Kind: "webhook", Config: json.RawMessage(`{"url":"ftp://x","format":"json"}`)}},
		{"channel missing chat_id", Destination{Name: "c", Kind: "channel", Config: json.RawMessage(`{"channel_id":"tg"}`)}},
		{"repo kind missing connector_id", Destination{Name: "g", Kind: "github", Config: json.RawMessage(`{}`)}},
		{"unknown kind", Destination{Name: "x", Kind: "whatsapp", Config: json.RawMessage(`{}`)}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := s.Create(t.Context(), tt.d)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("Create() = %v, want ErrInvalid", err)
			}
		})
	}
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

	t.Run("enabled automation reference refuses with ErrReferenced naming the automation", func(t *testing.T) {
		t.Parallel()
		s := &Store{}
		err := s.Delete(t.Context(), "d1", nil, fakeAutomationRefs{name: "daily-brief", referenced: true})
		if !errors.Is(err, ErrReferenced) {
			t.Fatalf("Delete = %v, want ErrReferenced", err)
		}
		if !bytes.Contains([]byte(err.Error()), []byte("daily-brief")) {
			t.Fatalf("Delete error %q does not name the referencing automation", err.Error())
		}
	})

	t.Run("automation reference lookup error propagates", func(t *testing.T) {
		t.Parallel()
		s := &Store{}
		err := s.Delete(t.Context(), "d1", nil, fakeAutomationRefs{err: errors.New("db down")})
		if err == nil || errors.Is(err, ErrReferenced) {
			t.Fatalf("Delete = %v, want a propagated (non-ErrReferenced) error", err)
		}
	})
}
