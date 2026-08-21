package destinations

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

// TestTelegramAdapterDeliverSendsDocumentOnlyWhenFilesPresent covers
// the files-only delivery contract: recipients want the mission's
// generated output, not a separate text body alongside it. The bold
// title + completion date go on the first file's caption instead of a
// standalone sendMessage call.
func TestTelegramAdapterDeliverSendsDocumentOnlyWhenFilesPresent(t *testing.T) {
	var gotMessages []map[string]any
	var gotDocuments []map[string]string
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
			gotDocuments = append(gotDocuments, map[string]string{
				"chat_id":    r.FormValue("chat_id"),
				"caption":    r.FormValue("caption"),
				"parse_mode": r.FormValue("parse_mode"),
			})
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
	payload := Payload{Name: "inbox-digest-8h", Body: "Mission complete: inbox-digest-8h", Files: []File{{Name: "out.txt", Data: []byte("data")}}}
	err := a.Deliver(t.Context(), json.RawMessage(`{"chat_id":"123"}`), "TG_BOT_TOKEN", payload)
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(gotMessages) != 0 {
		t.Fatalf("expected no sendMessage when files are present, got %+v", gotMessages)
	}
	if len(gotDocuments) != 1 || gotDocuments[0]["chat_id"] != "123" {
		t.Fatalf("expected one sendDocument to chat 123, got %v", gotDocuments)
	}
	if !strings.Contains(gotDocuments[0]["caption"], "inbox\\-digest\\-8h") {
		t.Fatalf("expected the bold title in the document caption, got %q", gotDocuments[0]["caption"])
	}
	if gotDocuments[0]["parse_mode"] != "MarkdownV2" {
		t.Fatalf("expected MarkdownV2 parse_mode on the document, got %+v", gotDocuments[0])
	}
}

// TestTelegramAdapterDeliverRendersTextArtifactsInline covers the
// primary case in the priority order: a .md/.txt declared artifact
// (payload.TextArtifacts) renders as formatted MarkdownV2 sendMessage
// calls, never a file attachment — even when Files is also non-empty,
// text artifacts win.
func TestTelegramAdapterDeliverRendersTextArtifactsInline(t *testing.T) {
	var gotMessages []map[string]any
	var gotDocuments []map[string]string
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
			gotDocuments = append(gotDocuments, map[string]string{"chat_id": r.FormValue("chat_id")})
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
	completedAt, err := time.Parse(time.RFC3339, "2026-08-21T20:30:00Z")
	if err != nil {
		t.Fatalf("parse fixture time: %v", err)
	}
	payload := Payload{
		Name:        "inbox-digest-8h",
		CompletedAt: completedAt,
		TextArtifacts: []TextArtifact{
			{Name: "digest.md", Content: "# Digest\n\nsomething **important**."},
		},
		// Files present too — text artifacts must still win the priority
		// order, this must never turn into a sendDocument call.
		Files: []File{{Name: "raw.csv", Data: []byte("a,b")}},
	}
	if err := a.Deliver(t.Context(), json.RawMessage(`{"chat_id":"123"}`), "TG_BOT_TOKEN", payload); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(gotDocuments) != 0 {
		t.Fatalf("expected no sendDocument when text artifacts are present, got %v", gotDocuments)
	}
	if len(gotMessages) != 1 || gotMessages[0]["chat_id"] != "123" {
		t.Fatalf("expected one sendMessage to chat 123, got %+v", gotMessages)
	}
	text, _ := gotMessages[0]["text"].(string)
	if !strings.Contains(text, "*inbox\\-digest\\-8h*") {
		t.Fatalf("expected the bold title heading the rendered content, got %q", text)
	}
	if !strings.Contains(text, "*Digest*") {
		t.Fatalf("expected the artifact's own heading rendered bold, got %q", text)
	}
	if !strings.Contains(text, "*important*") {
		t.Fatalf("expected the bold markdown converted to MarkdownV2, got %q", text)
	}
	if gotMessages[0]["parse_mode"] != "MarkdownV2" {
		t.Fatalf("expected MarkdownV2 parse_mode, got %+v", gotMessages[0])
	}
}

// TestTelegramAdapterDeliverSendsMessageWhenNoFiles covers the other
// leg: a payload with no artifacts still gets its short completion
// line as a plain sendMessage, since there's no file to caption.
func TestTelegramAdapterDeliverSendsMessageWhenNoFiles(t *testing.T) {
	var gotMessages []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			gotMessages = append(gotMessages, body)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	a := &TelegramAdapter{
		ResolveToken: fakeTokenResolver("TG_BOT_TOKEN", "secret-token"),
		APIBase:      srv.URL,
	}
	payload := Payload{Body: "Mission complete: inbox-digest-8h"}
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
}

func TestRenderTelegramCaption(t *testing.T) {
	completedAt, err := time.Parse(time.RFC3339, "2026-08-21T20:30:00Z")
	if err != nil {
		t.Fatalf("parse fixture time: %v", err)
	}
	p := Payload{Name: "inbox-digest-8h", CompletedAt: completedAt}
	got := renderTelegramCaption(p)
	if !strings.HasPrefix(got, "*inbox\\-digest\\-8h*\n") {
		t.Fatalf("expected a bold title line, got %q", got)
	}
	if !strings.Contains(got, "21 Aug 2026, 20:30 UTC") {
		t.Fatalf("expected the formatted completion date, got %q", got)
	}
}

func TestRenderTelegramCaptionOmitsDateWhenZero(t *testing.T) {
	got := renderTelegramCaption(Payload{Name: "ad-hoc send"})
	if strings.Contains(got, "\n") {
		t.Fatalf("expected no date line for a zero CompletedAt, got %q", got)
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
