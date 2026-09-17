package connectors

import (
	"encoding/json"
	"testing"
)

// TestRedactConfigHeaders pins D-115's read half: header values are
// blanked, header names and every other config key survive, and a
// config with no headers is handed back untouched.
func TestRedactConfigHeaders(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		cfg  string
		want string
	}{
		{
			name: "mcp headers are blanked, names and endpoint kept",
			cfg:  `{"endpoint":"https://mcp.example/x","headers":{"Authorization":"Bearer sk-live","x-api-key":"k-1"}}`,
			want: `{"endpoint":"https://mcp.example/x","headers":{"Authorization":"[redacted]","x-api-key":"[redacted]"}}`,
		},
		{
			name: "empty headers object stays empty",
			cfg:  `{"endpoint":"https://mcp.example/x","headers":{}}`,
			want: `{"endpoint":"https://mcp.example/x","headers":{}}`,
		},
		{
			name: "no headers key is untouched",
			cfg:  `{"client_id":"cid","scopes":["Mail.Read"]}`,
			want: `{"client_id":"cid","scopes":["Mail.Read"]}`,
		},
		{
			name: "empty config is untouched",
			cfg:  ``,
			want: ``,
		},
		{
			name: "non-object config is untouched",
			cfg:  `"not an object"`,
			want: `"not an object"`,
		},
		{
			name: "non-string header values are left alone rather than dropped",
			cfg:  `{"headers":{"X-Count":3}}`,
			want: `{"headers":{"X-Count":3}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := Connector{ID: "c1", Name: "srv", Kind: "mcp", Config: json.RawMessage(tc.cfg)}
			got := RedactConfigHeaders(in)
			assertSameJSON(t, string(got.Config), tc.want)
			// The caller's Connector must not be mutated.
			assertSameJSON(t, string(in.Config), tc.cfg)
		})
	}
}

// TestRestoreConfigHeaders pins D-115's write half: the round trip an
// unchanged form does must not overwrite a stored header with the
// placeholder, while a real edit still lands.
func TestRestoreConfigHeaders(t *testing.T) {
	t.Parallel()

	const stored = `{"endpoint":"https://mcp.example/x","headers":{"Authorization":"Bearer sk-live","x-api-key":"k-1"}}`

	for _, tc := range []struct {
		name            string
		current         string
		next            string
		want            string
		wantLeftoverRed bool
	}{
		{
			name:    "untouched round trip keeps both stored values",
			current: stored,
			next:    `{"endpoint":"https://mcp.example/x","headers":{"Authorization":"[redacted]","x-api-key":"[redacted]"}}`,
			want:    stored,
		},
		{
			name:    "editing one header keeps the other stored",
			current: stored,
			next:    `{"endpoint":"https://mcp.example/x","headers":{"Authorization":"Bearer sk-new","x-api-key":"[redacted]"}}`,
			want:    `{"endpoint":"https://mcp.example/x","headers":{"Authorization":"Bearer sk-new","x-api-key":"k-1"}}`,
		},
		{
			name:    "editing another field keeps the headers",
			current: stored,
			next:    `{"endpoint":"https://mcp.example/moved","headers":{"Authorization":"[redacted]","x-api-key":"[redacted]"}}`,
			want:    `{"endpoint":"https://mcp.example/moved","headers":{"Authorization":"Bearer sk-live","x-api-key":"k-1"}}`,
		},
		{
			name:    "a dropped header stays dropped",
			current: stored,
			next:    `{"endpoint":"https://mcp.example/x","headers":{"Authorization":"[redacted]"}}`,
			want:    `{"endpoint":"https://mcp.example/x","headers":{"Authorization":"Bearer sk-live"}}`,
		},
		{
			name:    "clearing every header is honored",
			current: stored,
			next:    `{"endpoint":"https://mcp.example/x","headers":{}}`,
			want:    `{"endpoint":"https://mcp.example/x","headers":{}}`,
		},
		{
			name:            "a placeholder on an unstored header is left for the caller to reject",
			current:         stored,
			next:            `{"endpoint":"https://mcp.example/x","headers":{"X-New":"[redacted]"}}`,
			want:            `{"endpoint":"https://mcp.example/x","headers":{"X-New":"[redacted]"}}`,
			wantLeftoverRed: true,
		},
		{
			name:            "nothing stored to restore from leaves the placeholder",
			current:         `{"endpoint":"https://mcp.example/x"}`,
			next:            `{"endpoint":"https://mcp.example/x","headers":{"Authorization":"[redacted]"}}`,
			want:            `{"endpoint":"https://mcp.example/x","headers":{"Authorization":"[redacted]"}}`,
			wantLeftoverRed: true,
		},
		{
			name:    "a config with no headers passes straight through",
			current: stored,
			next:    `{"client_id":"cid"}`,
			want:    `{"client_id":"cid"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			current := Connector{ID: "c1", Name: "srv", Kind: "mcp", Config: json.RawMessage(tc.current)}
			got := RestoreConfigHeaders(json.RawMessage(tc.next), current)
			assertSameJSON(t, string(got), tc.want)
			if leftover := HasRedactedHeader(got); leftover != tc.wantLeftoverRed {
				t.Errorf("HasRedactedHeader after restore = %v, want %v", leftover, tc.wantLeftoverRed)
			}
		})
	}
}

// TestRedactRestoreRoundTrip is the whole point of the pair: what the
// API hands out, handed straight back, must store exactly what was
// there before.
func TestRedactRestoreRoundTrip(t *testing.T) {
	t.Parallel()

	for _, cfg := range []string{
		`{"endpoint":"https://mcp.example/x","headers":{"Authorization":"Bearer sk-live"}}`,
		`{"endpoint":"https://mcp.example/x","headers":{}}`,
		`{"endpoint":"https://mcp.example/x","region":"eu-west-1","headers":{"a":"1","b":"2","c":"3"}}`,
		`{"client_id":"cid","scopes":["Mail.Read"]}`,
	} {
		t.Run(cfg, func(t *testing.T) {
			t.Parallel()
			stored := Connector{ID: "c1", Name: "srv", Kind: "mcp", Config: json.RawMessage(cfg)}
			served := RedactConfigHeaders(stored)
			back := RestoreConfigHeaders(served.Config, stored)
			assertSameJSON(t, string(back), cfg)
			if HasRedactedHeader(back) {
				t.Errorf("round trip left a placeholder behind: %s", back)
			}
		})
	}
}

// assertSameJSON compares two configs by decoded value, since map key
// order is not part of what either function promises.
func assertSameJSON(t *testing.T, got, want string) {
	t.Helper()
	if got == want {
		return
	}
	var g, w any
	gErr := json.Unmarshal([]byte(got), &g)
	wErr := json.Unmarshal([]byte(want), &w)
	if gErr != nil || wErr != nil {
		t.Fatalf("config = %s, want %s", got, want)
	}
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	if string(gb) != string(wb) {
		t.Fatalf("config = %s, want %s", got, want)
	}
}
