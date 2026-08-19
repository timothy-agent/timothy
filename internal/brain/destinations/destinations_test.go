package destinations

import (
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
