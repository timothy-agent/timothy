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

// sentMessages drives a fake Telegram Bot API and captures every
// sendMessage body, for tests that need to inspect what sendTelegramText
// actually sent (as opposed to renderTelegramText's pre-refactor pure
// rendering, which no longer exists now that headers/chunking need a
// live adapter call).
func sentMessages(t *testing.T, deliver func(a *TelegramAdapter)) []string {
	t.Helper()
	var texts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if text, ok := body["text"].(string); ok {
			texts = append(texts, text)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	deliver(&TelegramAdapter{ResolveToken: fakeTokenResolver("TG_BOT_TOKEN", "secret-token"), APIBase: srv.URL})
	return texts
}

func TestSendTelegramTextHeadsWithTitleAndDate(t *testing.T) {
	completedAt, err := time.Parse(time.RFC3339, "2026-08-21T20:30:00Z")
	if err != nil {
		t.Fatalf("parse fixture time: %v", err)
	}
	p := Payload{Name: "inbox-digest-8h", CompletedAt: completedAt, Body: "# heading\n\n**bold** text."}
	texts := sentMessages(t, func(a *TelegramAdapter) {
		if err := a.Deliver(t.Context(), json.RawMessage(`{"chat_id":"123"}`), "TG_BOT_TOKEN", p); err != nil {
			t.Fatalf("Deliver: %v", err)
		}
	})
	if len(texts) != 1 {
		t.Fatalf("expected one sendMessage, got %d: %v", len(texts), texts)
	}
	got := texts[0]
	if !strings.HasPrefix(got, "*inbox\\-digest\\-8h*\n21 Aug 2026, 20:30 UTC\n\n") {
		t.Fatalf("expected bold title + date head, got %q", got)
	}
	if strings.Contains(got, "**") {
		t.Fatalf("expected markdown converted (no literal **), got %q", got)
	}
	if !strings.Contains(got, "*bold*") {
		t.Fatalf("expected **bold** converted to MarkdownV2 bold, got %q", got)
	}
	if !strings.Contains(got, "*heading*") {
		t.Fatalf("expected # heading converted to a bold line, got %q", got)
	}
}

func TestSendTelegramTextUsesSubjectWhenNameEmpty(t *testing.T) {
	p := Payload{Subject: "Daily digest", Body: "the content"}
	texts := sentMessages(t, func(a *TelegramAdapter) {
		if err := a.Deliver(t.Context(), json.RawMessage(`{"chat_id":"123"}`), "TG_BOT_TOKEN", p); err != nil {
			t.Fatalf("Deliver: %v", err)
		}
	})
	if len(texts) != 1 {
		t.Fatalf("expected one sendMessage, got %d: %v", len(texts), texts)
	}
	if !strings.HasPrefix(texts[0], "*Daily digest*\n\nthe content") {
		t.Fatalf("got = %q, want subject as bold title", texts[0])
	}
}

func TestSendTelegramTextIncludesLinksAndOversizeNotice(t *testing.T) {
	p := Payload{Body: "digest", Links: []string{"https://timothy.example/missions/m1"}, OversizeFiles: []string{"huge.zip"}}
	texts := sentMessages(t, func(a *TelegramAdapter) {
		if err := a.Deliver(t.Context(), json.RawMessage(`{"chat_id":"123"}`), "TG_BOT_TOKEN", p); err != nil {
			t.Fatalf("Deliver: %v", err)
		}
	})
	if len(texts) != 1 {
		t.Fatalf("expected one sendMessage, got %d: %v", len(texts), texts)
	}
	if !strings.Contains(texts[0], `missions/m1`) {
		t.Fatalf("expected link content, got %q", texts[0])
	}
	if !strings.Contains(texts[0], escapeMarkdownV2("huge.zip")) {
		t.Fatalf("expected oversize file name in text, got %q", texts[0])
	}
}

func TestSendTelegramTextChunksLongBody(t *testing.T) {
	// A body long enough to force ChunkMarkdownV2 into multiple
	// sendMessage calls rather than a single truncated one.
	p := Payload{Name: "long-digest", Body: strings.Repeat("line of digest text.\n\n", 400)}
	texts := sentMessages(t, func(a *TelegramAdapter) {
		if err := a.Deliver(t.Context(), json.RawMessage(`{"chat_id":"123"}`), "TG_BOT_TOKEN", p); err != nil {
			t.Fatalf("Deliver: %v", err)
		}
	})
	if len(texts) < 2 {
		t.Fatalf("expected chunking into multiple messages, got %d: %v", len(texts), texts)
	}
	for i, chunk := range texts {
		if len(chunk) > telegramMessageLimit {
			t.Fatalf("chunk %d is %d bytes, want <= %d", i, len(chunk), telegramMessageLimit)
		}
	}
	if !strings.HasPrefix(texts[0], "*long\\-digest*") {
		t.Fatalf("expected the title heading only the first chunk, got %q", texts[0])
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

// TestTelegramAdapterDeliverNeverLeaksTokenOnTimeout covers the token
// leak: call() builds its request URL with the raw token, and a
// *url.Error from http.Client.Do embeds that URL verbatim. A server
// that never responds forces a client timeout so Do returns exactly
// that shape of error.
func TestTelegramAdapterDeliverNeverLeaksTokenOnTimeout(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	// Defers run LIFO: srv.Close() (declared second, runs first) would
	// otherwise block forever waiting for the still-blocked handler, so
	// close(block) must be declared AFTER srv.Close() to run BEFORE it.
	defer srv.Close()
	defer close(block)

	const token = "123456:super-secret-bot-token"
	a := &TelegramAdapter{
		ResolveToken: fakeTokenResolver("TG_BOT_TOKEN", token),
		APIBase:      srv.URL,
		HTTP:         &http.Client{Timeout: 50 * time.Millisecond},
	}
	err := a.Deliver(t.Context(), json.RawMessage(`{"chat_id":"123"}`), "TG_BOT_TOKEN", Payload{Body: "hi"})
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("error leaks the bot token: %q", err.Error())
	}
	if !errors.Is(err, errMaybeDelivered) {
		t.Fatalf("expected errMaybeDelivered for a client timeout, got %v", err)
	}
}

// TestTelegramAdapterDeliverNeverLeaksTokenOn500 covers the same
// redaction requirement on the non-2xx response path, and asserts the
// response is treated as maybe-delivered (retrying a processed request
// is pointless and a 5xx may still have side effects).
func TestTelegramAdapterDeliverNeverLeaksTokenOn500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal error"))
	}))
	defer srv.Close()

	const token = "123456:super-secret-bot-token"
	a := &TelegramAdapter{ResolveToken: fakeTokenResolver("TG_BOT_TOKEN", token), APIBase: srv.URL}
	err := a.Deliver(t.Context(), json.RawMessage(`{"chat_id":"123"}`), "TG_BOT_TOKEN", Payload{Body: "hi"})
	if err == nil {
		t.Fatal("expected an error for a 500 response")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("error leaks the bot token: %q", err.Error())
	}
	if !errors.Is(err, errMaybeDelivered) {
		t.Fatalf("expected errMaybeDelivered for a non-2xx response, got %v", err)
	}
}

// TestTelegramAdapterDeliverDialFailureIsSafeToRetry covers the other
// leg of classifySendErr: a connection that never got established
// (dialing a closed port) must NOT be errMaybeDelivered, since nothing
// left the machine and a retry cannot duplicate anything.
func TestTelegramAdapterDeliverDialFailureIsSafeToRetry(t *testing.T) {
	// A server that's immediately closed leaves its port refusing
	// connections, forcing a dial failure rather than any response.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	closedURL := srv.URL
	srv.Close()

	a := &TelegramAdapter{ResolveToken: fakeTokenResolver("TG_BOT_TOKEN", "secret-token"), APIBase: closedURL}
	err := a.Deliver(t.Context(), json.RawMessage(`{"chat_id":"123"}`), "TG_BOT_TOKEN", Payload{Body: "hi"})
	if err == nil {
		t.Fatal("expected an error dialing a closed port")
	}
	if errors.Is(err, errMaybeDelivered) {
		t.Fatalf("expected a dial failure to be safe-to-retry, got errMaybeDelivered: %v", err)
	}
}
