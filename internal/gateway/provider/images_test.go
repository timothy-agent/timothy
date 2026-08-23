package provider

import (
	"encoding/json"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

// imageMsg is a small helper: one user message carrying text plus one
// image (D-045's transient, request-build-time Images field).
func imageMsg(text, mediaType, data string) Message {
	return Message{Role: "user", Content: text, Images: []ImageData{{MediaType: mediaType, Data: data}}}
}

// --- openaicompat ---

func TestOpenAICompatImageMessageBuildsContentPartsArray(t *testing.T) {
	t.Parallel()
	o := NewOpenAICompat(OpenAICompatConfig{BaseURL: "http://x", APIKey: "k"})
	req := o.buildRequest(CompletionRequest{
		Model:    "m",
		Messages: []Message{imageMsg("what is this?", "image/png", "AAAA")},
	})

	if len(req.Messages) != 1 {
		t.Fatalf("got %d messages, want 1", len(req.Messages))
	}
	parts, ok := req.Messages[0].Content.([]oaiContentPart)
	if !ok {
		t.Fatalf("Content = %T, want []oaiContentPart", req.Messages[0].Content)
	}
	if len(parts) != 2 {
		t.Fatalf("got %d content parts, want 2 (text + image_url)", len(parts))
	}
	if parts[0].Type != "text" || parts[0].Text != "what is this?" {
		t.Fatalf("text part = %+v", parts[0])
	}
	if parts[1].Type != "image_url" || parts[1].ImageURL == nil {
		t.Fatalf("image part = %+v", parts[1])
	}
	wantURL := "data:image/png;base64,AAAA"
	if parts[1].ImageURL.URL != wantURL {
		t.Fatalf("image_url.url = %q, want %q", parts[1].ImageURL.URL, wantURL)
	}

	// The wire shape must serialize with a content-parts array, not a
	// bare string, for a message carrying images.
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded struct {
		Messages []struct {
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode wire JSON: %v (raw: %s)", err, data)
	}
	if len(decoded.Messages) != 1 || len(decoded.Messages[0].Content) != 2 {
		t.Fatalf("wire JSON content = %s, want a 2-element array", data)
	}
}

// TestOpenAICompatTextOnlyMessageUnchangedWireShape pins the existing
// wire shape: a message with no images marshals content as a bare
// string, exactly as before D-045 — some OpenAI-compat servers reject
// a content-parts array, so this must never regress.
func TestOpenAICompatTextOnlyMessageUnchangedWireShape(t *testing.T) {
	t.Parallel()
	o := NewOpenAICompat(OpenAICompatConfig{BaseURL: "http://x", APIKey: "k"})
	req := o.buildRequest(CompletionRequest{
		Model:    "m",
		Messages: []Message{{Role: "user", Content: "hello"}},
	})

	content, ok := req.Messages[0].Content.(string)
	if !ok || content != "hello" {
		t.Fatalf("Content = %+v, want the bare string %q", req.Messages[0].Content, "hello")
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode wire JSON (expected a plain string content): %v (raw: %s)", err, data)
	}
	if decoded.Messages[0].Content != "hello" {
		t.Fatalf("wire content = %q, want %q", decoded.Messages[0].Content, "hello")
	}
}

// --- anthropic ---

func TestAnthropicMessagesImageBlock(t *testing.T) {
	t.Parallel()
	msgs := anthropicMessages([]Message{imageMsg("what is this?", "image/jpeg", "BBBB")})

	if len(msgs) != 1 || msgs[0].Role != "user" {
		t.Fatalf("messages = %+v, want 1 user message", msgs)
	}
	blocks, ok := msgs[0].Content.([]anthropicContentBlock)
	if !ok {
		t.Fatalf("Content = %T, want []anthropicContentBlock", msgs[0].Content)
	}
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2 (text + image)", len(blocks))
	}
	if blocks[0].Type != "text" || blocks[0].Text != "what is this?" {
		t.Fatalf("text block = %+v", blocks[0])
	}
	if blocks[1].Type != "image" || blocks[1].Source == nil {
		t.Fatalf("image block = %+v", blocks[1])
	}
	if blocks[1].Source.Type != "base64" || blocks[1].Source.MediaType != "image/jpeg" || blocks[1].Source.Data != "BBBB" {
		t.Fatalf("image source = %+v", blocks[1].Source)
	}

	if _, err := json.Marshal(msgs); err != nil {
		t.Fatalf("marshal: %v", err)
	}
}

// TestAnthropicMessagesTextOnlyUnchangedWireShape pins the existing
// wire shape for a plain user/assistant message: content stays a bare
// string, not a block array, when there is nothing but text.
func TestAnthropicMessagesTextOnlyUnchangedWireShape(t *testing.T) {
	t.Parallel()
	msgs := anthropicMessages([]Message{{Role: "user", Content: "hello"}})

	if len(msgs) != 1 {
		t.Fatalf("messages = %+v, want 1", msgs)
	}
	content, ok := msgs[0].Content.(string)
	if !ok || content != "hello" {
		t.Fatalf("Content = %+v, want the bare string %q", msgs[0].Content, "hello")
	}
}

// --- bedrock ---

func TestConverseMessagesImageBlock(t *testing.T) {
	t.Parallel()
	out := converseMessages([]Message{imageMsg("what is this?", "image/webp", "Q0M=")}, true) // base64("CC")

	if len(out) != 1 || out[0].Role != types.ConversationRoleUser {
		t.Fatalf("messages = %+v, want 1 user message", out)
	}
	if len(out[0].Content) != 2 {
		t.Fatalf("got %d content blocks, want 2 (text + image)", len(out[0].Content))
	}
	if _, ok := out[0].Content[0].(*types.ContentBlockMemberText); !ok {
		t.Fatalf("block 0 = %#v, want text", out[0].Content[0])
	}
	imgBlock, ok := out[0].Content[1].(*types.ContentBlockMemberImage)
	if !ok {
		t.Fatalf("block 1 = %#v, want image", out[0].Content[1])
	}
	if imgBlock.Value.Format != types.ImageFormatWebp {
		t.Fatalf("image format = %v, want webp", imgBlock.Value.Format)
	}
	src, ok := imgBlock.Value.Source.(*types.ImageSourceMemberBytes)
	if !ok {
		t.Fatalf("image source = %#v, want bytes member", imgBlock.Value.Source)
	}
	if string(src.Value) != "CC" {
		t.Fatalf("decoded image bytes = %q, want %q", src.Value, "CC")
	}
}

// TestConverseMessagesTextOnlyUnchangedWireShape pins the existing
// shape: a plain user message keeps its single text block, no image
// block appended, when Images is empty.
func TestConverseMessagesTextOnlyUnchangedWireShape(t *testing.T) {
	t.Parallel()
	out := converseMessages([]Message{{Role: "user", Content: "hello"}}, true)

	if len(out) != 1 || len(out[0].Content) != 1 {
		t.Fatalf("messages = %+v, want 1 message with exactly 1 content block", out)
	}
	text, ok := out[0].Content[0].(*types.ContentBlockMemberText)
	if !ok || text.Value != "hello" {
		t.Fatalf("block = %#v, want text %q", out[0].Content[0], "hello")
	}
}

func TestBedrockImageFormatMapping(t *testing.T) {
	t.Parallel()
	cases := map[string]types.ImageFormat{
		"image/png":  types.ImageFormatPng,
		"image/jpeg": types.ImageFormatJpeg,
		"image/webp": types.ImageFormatWebp,
		"image/gif":  types.ImageFormatGif,
	}
	for mime, want := range cases {
		if got := bedrockImageFormat(mime); got != want {
			t.Fatalf("bedrockImageFormat(%q) = %v, want %v", mime, got, want)
		}
	}
}
