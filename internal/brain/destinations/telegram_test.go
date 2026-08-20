package destinations

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEscapeMarkdownV2(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"plain text unchanged", "hello world", "hello world"},
		{"each special char escaped", "_*[]()~`>#+-=|{}.!", `\_\*\[\]\(\)\~\` + "`" + `\>\#\+\-\=\|\{\}\.\!`},
		{"backslash escaped", `a\b`, `a\\b`},
		{"mixed prose", "done! see: https://x.example/a_b (link)", `done\! see: https://x\.example/a\_b \(link\)`},
		{"empty string", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := escapeMarkdownV2(tt.in); got != tt.want {
				t.Fatalf("escapeMarkdownV2(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestRenderTelegramTextUnderLimit(t *testing.T) {
	p := Payload{Body: "short digest.", Links: []string{"https://timothy.example/missions/m1"}}
	got := renderTelegramText(p)
	if len(got) > telegramMessageLimit {
		t.Fatalf("got %d bytes, want <= %d", len(got), telegramMessageLimit)
	}
	if !strings.Contains(got, "short digest") {
		t.Fatalf("expected digest content, got %q", got)
	}
	if !strings.Contains(got, `missions/m1`) {
		t.Fatalf("expected link content, got %q", got)
	}
}

func TestRenderTelegramTextPrependsSubjectWhenSet(t *testing.T) {
	p := Payload{Subject: "Daily digest", Body: "the content"}
	got := renderTelegramText(p)
	if !strings.HasPrefix(got, "Daily digest\n\nthe content") {
		t.Fatalf("got = %q, want subject prepended", got)
	}
}

func TestRenderTelegramTextTruncatesOverLimit(t *testing.T) {
	p := Payload{Body: strings.Repeat("x", telegramMessageLimit*2), Links: []string{"https://timothy.example/missions/m1"}}
	got := renderTelegramText(p)
	if len(got) > telegramMessageLimit {
		t.Fatalf("truncated text is %d bytes, want <= %d", len(got), telegramMessageLimit)
	}
	if !strings.Contains(got, "truncated") {
		t.Fatalf("expected a truncation notice, got tail %q", got[len(got)-80:])
	}
	if !strings.Contains(got, "missions/m1") {
		t.Fatalf("expected the mission link preserved after truncation, got tail %q", got[len(got)-200:])
	}
}

func TestRenderTelegramTextTruncationRespectsMultibyteRunes(t *testing.T) {
	// A body made entirely of multi-byte runes must not panic or corrupt
	// output when cut for length.
	p := Payload{Body: strings.Repeat("测试", telegramMessageLimit)}
	got := renderTelegramText(p)
	if len(got) > telegramMessageLimit {
		t.Fatalf("got %d bytes, want <= %d", len(got), telegramMessageLimit)
	}
	if !strings.HasPrefix(got, "测试") {
		t.Fatalf("expected valid utf8 prefix preserved, got %q", got[:min(20, len(got))])
	}
}

func TestRenderTelegramTextIncludesOversizeNotice(t *testing.T) {
	p := Payload{Body: "digest", OversizeFiles: []string{"huge.zip"}}
	got := renderTelegramText(p)
	// The oversize notice text is itself MarkdownV2-escaped, so the
	// file name's literal dot comes through escaped too.
	if !strings.Contains(got, escapeMarkdownV2("huge.zip")) {
		t.Fatalf("expected oversize file name in text, got %q", got)
	}
}

// fakeTokenResolver resolves exactly one ref to one value, erroring on
// anything else.
func fakeTokenResolver(ref, value string) tokenResolver {
	return func(_ context.Context, r string) (string, error) {
		if r != ref {
			return "", errors.New("unknown ref")
		}
		return value, nil
	}
}

func TestTelegramAdapterDeliverSendsMessageAndDocument(t *testing.T) {
	var gotMessages []map[string]any
	var gotDocuments []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			gotMessages = append(gotMessages, body)
		case strings.HasSuffix(r.URL.Path, "/sendDocument"):
			if err := r.ParseMultipartForm(1 << 20); err != nil { //nolint:gosec // G120: test server, fixed small fixture body
				t.Fatalf("parse multipart: %v", err)
			}
			gotDocuments = append(gotDocuments, r.FormValue("chat_id"))
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	a := &TelegramAdapter{
		ResolveToken: fakeTokenResolver("TG_BOT_TOKEN", "secret-token"),
		APIBase:      srv.URL,
	}
	payload := Payload{Body: "mission done", Files: []File{{Name: "out.txt", Data: []byte("data")}}}
	err := a.Deliver(t.Context(), json.RawMessage(`{"chat_id":"123"}`), "TG_BOT_TOKEN", payload)
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(gotMessages) != 1 || gotMessages[0]["chat_id"] != "123" {
		t.Fatalf("expected one sendMessage to chat 123, got %+v", gotMessages)
	}
	if gotMessages[0]["parse_mode"] != "MarkdownV2" {
		t.Fatalf("expected MarkdownV2 parse_mode, got %+v", gotMessages[0])
	}
	if len(gotDocuments) != 1 || gotDocuments[0] != "123" {
		t.Fatalf("expected one sendDocument to chat 123, got %v", gotDocuments)
	}
}

func TestTelegramAdapterDeliverMissingCredentialRef(t *testing.T) {
	a := &TelegramAdapter{ResolveToken: fakeTokenResolver("TG_BOT_TOKEN", "secret-token")}
	err := a.Deliver(t.Context(), json.RawMessage(`{"chat_id":"123"}`), "", Payload{})
	if err == nil {
		t.Fatal("expected error for missing credential_ref")
	}
}

func TestTelegramAdapterDeliverResolveTokenFails(t *testing.T) {
	a := &TelegramAdapter{ResolveToken: fakeTokenResolver("OTHER_REF", "secret-token")}
	err := a.Deliver(t.Context(), json.RawMessage(`{"chat_id":"123"}`), "TG_BOT_TOKEN", Payload{})
	if err == nil {
		t.Fatal("expected error when the token resolver fails")
	}
}

func TestTelegramAdapterDeliverAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":false,"description":"chat not found"}`))
	}))
	defer srv.Close()

	a := &TelegramAdapter{ResolveToken: fakeTokenResolver("TG_BOT_TOKEN", "secret-token"), APIBase: srv.URL}
	err := a.Deliver(t.Context(), json.RawMessage(`{"chat_id":"123"}`), "TG_BOT_TOKEN", Payload{Body: "hi"})
	if err == nil || !strings.Contains(err.Error(), "chat not found") {
		t.Fatalf("expected api error surfaced, got %v", err)
	}
}

func TestTelegramAdapterDeliverBadConfig(t *testing.T) {
	a := &TelegramAdapter{ResolveToken: fakeTokenResolver("TG_BOT_TOKEN", "secret-token")}
	err := a.Deliver(t.Context(), json.RawMessage(`not json`), "TG_BOT_TOKEN", Payload{})
	if err == nil {
		t.Fatal("expected error for malformed config")
	}
}
