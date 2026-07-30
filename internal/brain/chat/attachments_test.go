package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/attachments"
	"github.com/SumonMSelim/timothy/internal/brain/session"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// fakeAttachments is an in-memory AttachmentStore fake: ids map
// directly to fixed bytes/mime pairs seeded by the test.
type fakeAttachments struct {
	byID map[string]attachments.Attachment
	data map[string][]byte
}

func newFakeAttachments() *fakeAttachments {
	return &fakeAttachments{byID: map[string]attachments.Attachment{}, data: map[string][]byte{}}
}

func (f *fakeAttachments) seed(id, mime string, data []byte) {
	f.byID[id] = attachments.Attachment{ID: id, Mime: mime, SizeBytes: int64(len(data))}
	f.data[id] = data
}

func (f *fakeAttachments) Get(_ context.Context, id string) (attachments.Attachment, error) {
	a, ok := f.byID[id]
	if !ok {
		return attachments.Attachment{}, attachments.ErrNotFound
	}
	return a, nil
}

func (f *fakeAttachments) Open(_ context.Context, id string) (io.ReadCloser, attachments.Attachment, error) {
	a, ok := f.byID[id]
	if !ok {
		return nil, attachments.Attachment{}, attachments.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(f.data[id])), a, nil
}

// TestChatAcceptsAttachmentOnlyMessage confirms an empty Message with
// at least one attachment ref is a valid turn, not a 400 — attachments
// alone are a legitimate reason to send a turn.
func TestChatAcceptsAttachmentOnlyMessage(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	gw := &fakeGW{events: okEvents("I see an image")}
	svc := newService(gw, log)
	fa := newFakeAttachments()
	fa.seed("abc123", "image/png", []byte("fake-png-bytes"))
	svc.SetAttachments(fa)

	_, ch, err := svc.Chat(t.Context(), Request{SessionID: "s1", Attachments: []string{"abc123"}})
	if err != nil {
		t.Fatalf("Chat with no text but an attachment: %v", err)
	}
	drain(t, ch)
	waitFor(t, func() bool { return len(log.kinds("s1")) == 3 })
}

// TestChatRejectsEmptyMessageWithNoAttachments confirms the original
// empty-message validation still fires when there is also no
// attachment to justify the turn.
func TestChatRejectsEmptyMessageWithNoAttachments(t *testing.T) {
	t.Parallel()
	svc := newService(&fakeGW{}, newFakeLog())
	if _, _, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "   "}); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("err = %v, want ErrBadRequest", err)
	}
}

// TestChatRejectsUnknownAttachmentBeforeAnyAppend confirms a bad
// attachment id 400s BEFORE the user_message (or even session_started
// on a fresh session) is appended — D-042's "store lookups before
// turnBegin" ordering extended to attachment validation.
func TestChatRejectsUnknownAttachmentBeforeAnyAppend(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	svc := newService(&fakeGW{}, log)
	svc.SetAttachments(newFakeAttachments()) // enabled, but "missing" is unseeded

	_, _, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "look", Attachments: []string{"missing"}})
	if !errors.Is(err, ErrBadRequest) {
		t.Fatalf("err = %v, want ErrBadRequest", err)
	}
	if kinds := log.kinds("s1"); len(kinds) != 0 {
		t.Fatalf("events appended despite bad ref: %v", kinds)
	}
}

// TestChatRejectsAttachmentsWhenDisabled confirms a Request naming
// attachment ids 400s when no store is wired (ATTACHMENTS_DIR unset).
func TestChatRejectsAttachmentsWhenDisabled(t *testing.T) {
	t.Parallel()
	svc := newService(&fakeGW{}, newFakeLog())
	// SetAttachments never called: s.attachments stays nil.
	_, _, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "look", Attachments: []string{"x"}})
	if !errors.Is(err, ErrBadRequest) {
		t.Fatalf("err = %v, want ErrBadRequest", err)
	}
}

// TestChatImageRefsLandInUserMessageEvent confirms the persisted
// user_message event carries the image refs (id+mime), and that the
// gateway request the driver sees has the ref resolved into base64
// Images — never a ref living on the wire request itself.
func TestChatImageRefsLandInUserMessageEvent(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	gw := &fakeGW{events: okEvents("described")}
	svc := newService(gw, log)
	fa := newFakeAttachments()
	fa.seed("img1", "image/png", []byte("raw-bytes"))
	svc.SetAttachments(fa)

	_, ch, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "what is this?", Attachments: []string{"img1"}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)
	waitFor(t, func() bool { return len(log.kinds("s1")) == 3 })

	events, _ := log.Events(t.Context(), "s1")
	var um session.UserMessage
	if err := json.Unmarshal(events[1].Payload, &um); err != nil {
		t.Fatalf("decode user_message: %v", err)
	}
	if len(um.Images) != 1 || um.Images[0].ID != "img1" || um.Images[0].Mime != "image/png" {
		t.Fatalf("persisted images = %+v, want one ref to img1/image/png", um.Images)
	}

	sent := chatRequest(t, gw)
	if len(sent.Messages) == 0 {
		t.Fatal("no messages sent to gateway")
	}
	last := sent.Messages[len(sent.Messages)-1]
	if len(last.Images) != 1 {
		t.Fatalf("gateway message Images = %+v, want 1 resolved image", last.Images)
	}
	if last.Images[0].MediaType != "image/png" {
		t.Fatalf("resolved image MediaType = %q, want image/png", last.Images[0].MediaType)
	}
	wantB64 := "cmF3LWJ5dGVz" // base64("raw-bytes")
	if last.Images[0].Data != wantB64 {
		t.Fatalf("resolved image Data = %q, want %q", last.Images[0].Data, wantB64)
	}
	if len(last.ImageRefs) != 0 {
		t.Fatalf("ImageRefs not cleared after resolution: %+v", last.ImageRefs)
	}
}

// TestRetryReplaysImageRefs confirms a retried turn's gateway request
// still carries the original message's images — Retry never touches
// the persisted event, so LLMContext re-projects the same refs and
// runTurn resolves them again from the attachment store.
func TestRetryReplaysImageRefs(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	gw := &fakeGW{events: []stream.StreamEvent{{Type: stream.EventError, Err: &stream.StreamError{Code: "boom", Message: "boom"}}}}
	svc := newService(gw, log)
	fa := newFakeAttachments()
	fa.seed("img1", "image/webp", []byte("webp-data"))
	svc.SetAttachments(fa)

	_, ch, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "q", Attachments: []string{"img1"}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)
	waitFor(t, func() bool { return len(log.kinds("s1")) == 3 }) // session_started, user_message, turn_failed
	waitFor(t, func() bool { return !svc.TurnActive("s1") })

	gw.mu.Lock()
	gw.events = okEvents("retried answer")
	gw.mu.Unlock()

	_, ch, err = svc.Retry(t.Context(), "s1")
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	drain(t, ch)

	sent := gw.lastChatRequest()
	if len(sent.Messages) == 0 {
		t.Fatal("no messages sent on retry")
	}
	// The original user message (carrying the image) is Messages[0];
	// a failed-attempt note from the dead first try's turn_failed
	// event may follow it as a separate user-role message, per
	// LLMContext's projection — the image rides on the ORIGINAL
	// message, not whatever happens to be last.
	first := sent.Messages[0]
	if len(first.Images) != 1 || first.Images[0].MediaType != "image/webp" {
		t.Fatalf("retried message Images = %+v, want the original image replayed", first.Images)
	}
}

// TestChatImageMessageAutoFlipsToVisionRoute is the D-046 feature under
// test: a message carrying an attachment with no explicit route picked
// gets routed to "vision" instead of riding the agent's/default chain.
func TestChatImageMessageAutoFlipsToVisionRoute(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	gw := &fakeGW{events: okEvents("described")}
	svc := newService(gw, log)
	fa := newFakeAttachments()
	fa.seed("img1", "image/png", []byte("bytes"))
	svc.SetAttachments(fa)

	_, ch, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "what is this?", Attachments: []string{"img1"}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)

	sent := gw.lastChatRequest()
	if sent.Route != "vision" {
		t.Fatalf("route = %q, want vision (image message, no explicit route)", sent.Route)
	}
}

// TestChatImageMessageWithExplicitRouteIsHonored confirms the request's
// own route pick outranks the vision auto-flip — precedence #2 beats #3.
func TestChatImageMessageWithExplicitRouteIsHonored(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	gw := &fakeGW{events: okEvents("described")}
	svc := newService(gw, log)
	fa := newFakeAttachments()
	fa.seed("img1", "image/png", []byte("bytes"))
	svc.SetAttachments(fa)

	_, ch, err := svc.Chat(t.Context(), Request{
		SessionID: "s1", Message: "what is this?", Attachments: []string{"img1"}, Route: "research",
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)

	sent := gw.lastChatRequest()
	if sent.Route != "research" {
		t.Fatalf("route = %q, want research (explicit request route wins over the vision flip)", sent.Route)
	}
}

// TestChatImageMessageOnSensitivePinnedSessionKeepsPinnedRoute confirms
// the session-wide sensitive pin (precedence #1) beats the vision flip:
// an image message on a pinned session stays on the pinned route even
// though it may then correctly fail the gateway's vision capability gate.
func TestChatImageMessageOnSensitivePinnedSessionKeepsPinnedRoute(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	seedSensitiveSession(t, log, "s1")
	gw := &fakeGW{events: okEvents("described")}
	svc := newService(gw, log)
	svc.SetSensitiveTools(&session.SensitiveTools{Suffixes: []string{"gmail_read"}, Route: func(context.Context) string { return "local" }})
	fa := newFakeAttachments()
	fa.seed("img1", "image/png", []byte("bytes"))
	svc.SetAttachments(fa)

	_, ch, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "what is this?", Attachments: []string{"img1"}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)

	sent := gw.lastChatRequest()
	if sent.Route != "local" {
		t.Fatalf("route = %q, want local (sensitive pin beats the vision flip)", sent.Route)
	}
}

// TestChatNoImagesLeavesRouteUnchanged confirms a plain-text message
// never triggers the vision flip.
func TestChatNoImagesLeavesRouteUnchanged(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	gw := &fakeGW{events: okEvents("hi")}
	svc := newService(gw, log)

	_, ch, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "hello"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)

	sent := gw.lastChatRequest()
	if sent.Route != defaultRoute {
		t.Fatalf("route = %q, want %q (no images, no flip)", sent.Route, defaultRoute)
	}
}

// TestRetryOfImageTurnStaysOnVisionRoute confirms Retry reuses the
// persisted (already-flipped) route: the vision flip happens once at
// Chat time and is durable on the user_message event, so a retried
// image turn never needs its own vision-detection logic.
func TestRetryOfImageTurnStaysOnVisionRoute(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	gw := &fakeGW{events: []stream.StreamEvent{{Type: stream.EventError, Err: &stream.StreamError{Code: "boom", Message: "boom"}}}}
	svc := newService(gw, log)
	fa := newFakeAttachments()
	fa.seed("img1", "image/png", []byte("bytes"))
	svc.SetAttachments(fa)

	_, ch, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "what is this?", Attachments: []string{"img1"}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)
	waitFor(t, func() bool { return !svc.TurnActive("s1") })

	gw.mu.Lock()
	gw.events = okEvents("retried answer")
	gw.mu.Unlock()

	_, ch, err = svc.Retry(t.Context(), "s1")
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	drain(t, ch)

	sent := gw.lastChatRequest()
	if sent.Route != "vision" {
		t.Fatalf("retried route = %q, want vision (persisted route replayed)", sent.Route)
	}
}
