package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// BedrockConfig configures Amazon Bedrock via the Converse API. Chat,
// streaming, and tool calling target first-party Amazon models only
// (Nova and Titan families) so usage bills against AWS credits — no
// third-party models.
//
// D-047/D-048: static IAM keys in the secret store are the only
// supported auth — AWS profile/SSO mode was removed (a headless server
// has no ~/.aws to mount). credential_ref must resolve through the
// encrypted secret store to JSON matching StaticCredentials; a resolve
// failure or a parse failure both fail provider construction (config
// honesty), never a silent fallback. Region precedence: the secret
// JSON's own "region" field, when set, wins over the provider's
// options.region (Region below); with neither set, us-east-1 applies.
//
// Providers table example:
//
//	driver = 'bedrock'
//	default_model = 'us.amazon.nova-pro-v1:0'    -- inference-profile ID
//	credential_ref = 'bedrock-static'            -- secret store ref holding StaticCredentials JSON
//	options = '{"region": "us-west-2"}'          -- optional; defaults to us-east-1
//
// Models must be enabled on the Bedrock console "Model access" page.
type BedrockConfig struct {
	Name   string
	Region string // from options.region; defaults to us-east-1 unless StaticCredentials.Region overrides it
	// StaticCredentials is the resolved secret-store value for
	// credential_ref parsed as JSON — required; a non-nil pointer with an
	// empty AccessKeyID/SecretAccessKey never happens, since
	// ParseStaticCredentials rejects that at parse time.
	StaticCredentials *StaticCredentials
	Timeout           time.Duration
}

// StaticCredentials is the secret-store JSON shape for Bedrock static
// IAM keys: {"access_key_id":"...","secret_access_key":"...","session_token":"(optional)","region":"(optional)"}.
// Unknown keys are ignored; AccessKeyID/SecretAccessKey are required.
type StaticCredentials struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	SessionToken    string `json:"session_token,omitempty"`
	Region          string `json:"region,omitempty"`
}

// ParseStaticCredentials parses a secret-store value as Bedrock static
// credentials JSON. A missing access_key_id or secret_access_key is a
// parse error — a resolved secret that isn't usable credentials must
// fail loudly, never fall back to profile/SSO silently.
func ParseStaticCredentials(raw string) (*StaticCredentials, error) {
	var c StaticCredentials
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return nil, fmt.Errorf("bedrock: parse static credentials: %w", err)
	}
	if c.AccessKeyID == "" || c.SecretAccessKey == "" {
		return nil, fmt.Errorf("bedrock: static credentials missing access_key_id or secret_access_key")
	}
	return &c, nil
}

// Bedrock is a Provider implementation backed by Amazon Bedrock.
type Bedrock struct {
	cfg BedrockConfig

	initOnce  sync.Once
	client    *bedrockruntime.Client
	clientErr error
}

func NewBedrock(cfg BedrockConfig) *Bedrock {
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	return &Bedrock{cfg: cfg}
}

func (b *Bedrock) Name() string { return b.cfg.Name }
func (b *Bedrock) Kind() Kind   { return KindAPI }

// HasStaticCredentials reports whether this provider was built with a
// resolved secret-store credential (D-047) — always true since profile
// mode was removed (D-048); used by router tests to verify the lookup
// plumbing reaches the bedrock constructor.
func (b *Bedrock) HasStaticCredentials() bool { return b.cfg.StaticCredentials != nil }

func (b *Bedrock) Capabilities() []Capability {
	return []Capability{CapChat, CapStreaming, CapTools, CapEmbeddings, CapVision}
}

// lazyClient builds the AWS client on first use. Region precedence is
// secret JSON region > BedrockConfig.Region (options.region, already
// defaulted to us-east-1 by NewBedrock unless a driver deriving path set
// it) — a static-keys row with no region anywhere is a construction
// error, since there is no profile/env to derive it from. sync.Once
// makes it safe under concurrent Stream/Embed calls; a load failure is
// sticky, which is right — bad credentials or a bad region never heal
// mid-process.
func (b *Bedrock) lazyClient(ctx context.Context) (*bedrockruntime.Client, error) {
	b.initOnce.Do(func() {
		sc := b.cfg.StaticCredentials
		region := sc.Region
		if region == "" {
			region = b.cfg.Region
		}
		if region == "" {
			b.clientErr = fmt.Errorf("bedrock: static credentials require a region (set the secret JSON's %q field or the provider's options.region)", "region")
			return
		}
		awsCfg, err := config.LoadDefaultConfig(ctx,
			config.WithRegion(region),
			config.WithCredentialsProvider(
				credentials.NewStaticCredentialsProvider(sc.AccessKeyID, sc.SecretAccessKey, sc.SessionToken)),
		)
		if err != nil {
			b.clientErr = fmt.Errorf("bedrock: load aws config: %w", err)
			return
		}
		b.client = bedrockruntime.NewFromConfig(awsCfg)
	})
	return b.client, b.clientErr
}

// Stream implements the normalized streaming contract using Bedrock
// ConverseStream. req.Effort is ignored: Converse exposes no
// reasoning-effort dial for Nova/Titan models.
func (b *Bedrock) Stream(ctx context.Context, req CompletionRequest) (<-chan stream.StreamEvent, error) {
	client, err := b.lazyClient(ctx)
	if err != nil {
		return nil, err
	}

	input := buildConverseStreamInput(req)

	outCh := make(chan stream.StreamEvent, 64)

	go func() {
		defer close(outCh)
		// Hard per-request timeout, same contract as the HTTP drivers: a
		// hung stream must end, not wedge the consumer forever.
		sctx, cancel := context.WithTimeout(ctx, b.cfg.Timeout)
		defer cancel()

		resp, err := client.ConverseStream(sctx, input)
		if err != nil {
			emit(sctx, outCh, stream.StreamEvent{Type: stream.EventError, Err: &stream.StreamError{
				Code:      "bedrock_error",
				Message:   err.Error(),
				Retryable: true,
			}})
			return
		}
		events := resp.GetStream()
		defer func() { _ = events.Close() }()

		var currentToolID, currentToolName string
		var toolInput []byte
		splitter := &thinkTagSplitter{}

		for event := range events.Events() {
			switch v := event.(type) {
			case *types.ConverseStreamOutputMemberContentBlockDelta:
				if delta, ok := v.Value.Delta.(*types.ContentBlockDeltaMemberText); ok {
					for _, ev := range splitter.Feed(delta.Value) {
						if !emit(sctx, outCh, ev) {
							return
						}
					}
				}
				if toolDelta, ok := v.Value.Delta.(*types.ContentBlockDeltaMemberToolUse); ok {
					if toolDelta.Value.Input != nil {
						toolInput = append(toolInput, []byte(*toolDelta.Value.Input)...)
					}
				}

			case *types.ConverseStreamOutputMemberContentBlockStart:
				if start, ok := v.Value.Start.(*types.ContentBlockStartMemberToolUse); ok {
					currentToolID = aws.ToString(start.Value.ToolUseId)
					currentToolName = aws.ToString(start.Value.Name)
					toolInput = nil
					if !emit(sctx, outCh, stream.StreamEvent{
						Type: stream.EventToolStart,
						ToolCall: &stream.ToolCallEvent{
							ID:   currentToolID,
							Name: currentToolName,
						},
					}) {
						return
					}
				}

			case *types.ConverseStreamOutputMemberContentBlockStop:
				for _, ev := range splitter.Flush() {
					if !emit(sctx, outCh, ev) {
						return
					}
				}
				if currentToolID != "" && currentToolName != "" {
					if len(toolInput) == 0 {
						// Zero-argument calls still need valid JSON downstream.
						toolInput = []byte("{}")
					}
					if !emit(sctx, outCh, stream.StreamEvent{
						Type: stream.EventToolEnd,
						ToolCall: &stream.ToolCallEvent{
							ID:    currentToolID,
							Name:  currentToolName,
							Input: json.RawMessage(toolInput),
						},
					}) {
						return
					}
					currentToolID = ""
					currentToolName = ""
					toolInput = nil
				}

			case *types.ConverseStreamOutputMemberMessageStop:
				// A max-tokens stop is a truncated answer; consumers
				// (compactor convergence, agent loop) key on this event.
				if v.Value.StopReason == types.StopReasonMaxTokens {
					if !emit(sctx, outCh, stream.StreamEvent{Type: stream.EventIncomplete, Text: "stop_reason=max_tokens"}) {
						return
					}
				}

			case *types.ConverseStreamOutputMemberMetadata:
				if v.Value.Usage != nil {
					if !emit(sctx, outCh, stream.StreamEvent{
						Type: stream.EventUsage,
						Usage: &stream.Usage{
							InputTokens:      int(aws.ToInt32(v.Value.Usage.InputTokens)),
							OutputTokens:     int(aws.ToInt32(v.Value.Usage.OutputTokens)),
							CacheReadTokens:  int(aws.ToInt32(v.Value.Usage.CacheReadInputTokens)),
							CacheWriteTokens: int(aws.ToInt32(v.Value.Usage.CacheWriteInputTokens)),
						},
					}) {
						return
					}
				}
			}
		}

		for _, ev := range splitter.Flush() {
			if !emit(sctx, outCh, ev) {
				return
			}
		}
		// A closed event channel is not success: a mid-stream failure
		// surfaces only via Err(). Without this check a dropped
		// connection would masquerade as a clean EventDone.
		if err := events.Err(); err != nil {
			emit(sctx, outCh, stream.StreamEvent{Type: stream.EventError, Err: &stream.StreamError{
				Code:      "bedrock_stream",
				Message:   err.Error(),
				Retryable: true,
			}})
			return
		}
		emit(sctx, outCh, stream.StreamEvent{Type: stream.EventDone})
	}()

	return outCh, nil
}

// buildConverseStreamInput assembles the ConverseStream request.
// Nova models are served through inference profiles, so ModelId is
// e.g. "us.amazon.nova-pro-v1:0" — the bare model ID rejects
// on-demand invocation.
//
// D-037: Nova tool-calling streams intermittently abort mid-stream
// with "ModelStreamErrorException: Model produced invalid sequence as
// part of ToolUse". AWS's Nova tool-troubleshooting guide prescribes
// greedy decoding (temperature 0, topK 1) for any request that offers
// tools — sampling variance is what produces the malformed ToolUse
// sequence. topK has no field on InferenceConfiguration; Nova reads it
// from AdditionalModelRequestFields instead. Titan has no such
// failure mode and no topK field, so the workaround is gated to Nova.
func buildConverseStreamInput(req CompletionRequest) *bedrockruntime.ConverseStreamInput {
	toolConfig := converseTools(req.Tools)
	input := &bedrockruntime.ConverseStreamInput{
		ModelId: aws.String(req.Model),
		// hasTools gates whether tool call/result content blocks are
		// legal to emit at all: Bedrock's ValidationException
		// "The toolConfig field must be defined when using toolUse and
		// toolResult content blocks" fires whenever such blocks appear
		// on a request with no ToolConfig — e.g. the brain loop's
		// force-synthesis turn nils Tools but keeps prior tool-call/
		// tool-result history in messages. When no tools are offered,
		// converseMessages flattens that history to plain text instead.
		Messages:   converseMessages(req.Messages, toolConfig != nil),
		ToolConfig: toolConfig,
		System:     converseSystem(req.System, req.Model),
	}
	if req.MaxTokens > 0 {
		input.InferenceConfig = &types.InferenceConfiguration{
			MaxTokens: aws.Int32(int32(req.MaxTokens)), //nolint:gosec // token caps are far below int32 max
		}
	}
	if len(req.Tools) > 0 && strings.Contains(req.Model, "amazon.nova") {
		if input.InferenceConfig == nil {
			input.InferenceConfig = &types.InferenceConfiguration{}
		}
		input.InferenceConfig.Temperature = aws.Float32(0)
		input.AdditionalModelRequestFields = document.NewLazyDocument(map[string]any{
			"inferenceConfig": map[string]any{"topK": 1},
		})
	}
	if req.ForceTool != "" && input.ToolConfig != nil && converseSupportsToolChoice(req.Model) {
		input.ToolConfig.ToolChoice = &types.ToolChoiceMemberTool{
			Value: types.SpecificToolChoice{Name: aws.String(req.ForceTool)},
		}
	}
	return input
}

// converseSupportsToolChoice reports whether Converse's ToolChoice
// field is honored for this model (D-063): Anthropic and Mistral
// Large support forced tool choice; Amazon Nova does not, so
// CompletionRequest.ForceTool is ignored there (graceful degrade).
func converseSupportsToolChoice(model string) bool {
	return strings.Contains(model, "anthropic.") || strings.Contains(model, "mistral-large")
}

// converseMessages maps normalized messages onto Bedrock Converse
// messages. Tool results ride as user-role toolResult blocks;
// assistant messages that carry neither text nor tool calls are
// dropped — Converse rejects empty content blocks.
//
// hasTools reports whether the request's ToolConfig will be non-nil.
// Bedrock rejects toolUse/toolResult content blocks outright when no
// ToolConfig is sent ("The toolConfig field must be defined when using
// toolUse and toolResult content blocks"), so when hasTools is false
// any tool call / tool result in the history is flattened to plain
// text instead of emitted as a toolUse/toolResult block. When hasTools
// is true, behavior is unchanged.
func converseMessages(msgs []Message, hasTools bool) []types.Message {
	out := make([]types.Message, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case "user":
			blocks := []types.ContentBlock{&types.ContentBlockMemberText{Value: m.Content}}
			// Text-only messages keep the single text block (unchanged);
			// only a message actually carrying images pays for decoding
			// them (D-045). Bytes only exist here transiently, decoded
			// straight from the base64 chat.runTurn filled in.
			for _, img := range m.Images {
				raw, err := base64.StdEncoding.DecodeString(img.Data)
				if err != nil {
					continue // malformed image data must not abort the whole turn
				}
				blocks = append(blocks, &types.ContentBlockMemberImage{Value: types.ImageBlock{
					Format: bedrockImageFormat(img.MediaType),
					Source: &types.ImageSourceMemberBytes{Value: raw},
				}})
			}
			out = append(out, types.Message{
				Role:    types.ConversationRoleUser,
				Content: blocks,
			})
		case "assistant":
			blocks := []types.ContentBlock{}
			if m.Content != "" {
				blocks = append(blocks, &types.ContentBlockMemberText{Value: m.Content})
			}
			for _, tc := range m.ToolCalls {
				if !hasTools {
					// No ToolConfig on this request: fold the call into
					// text instead of a toolUse block Bedrock would reject.
					blocks = append(blocks, &types.ContentBlockMemberText{
						Value: flattenToolCallText(tc),
					})
					continue
				}
				blocks = append(blocks, &types.ContentBlockMemberToolUse{
					Value: types.ToolUseBlock{
						ToolUseId: aws.String(tc.ID),
						Name:      aws.String(tc.Name),
						Input:     documentFromJSON(tc.Input),
					},
				})
			}
			if len(blocks) == 0 {
				continue
			}
			out = append(out, types.Message{
				Role:    types.ConversationRoleAssistant,
				Content: blocks,
			})
		case "tool":
			if m.ToolResult == nil {
				continue
			}
			if !hasTools {
				// No ToolConfig on this request: a toolResult block is
				// rejected outright, so flatten to plain user text.
				out = append(out, types.Message{
					Role: types.ConversationRoleUser,
					Content: []types.ContentBlock{&types.ContentBlockMemberText{
						Value: flattenToolResultText(*m.ToolResult),
					}},
				})
				continue
			}
			resultBlock := types.ContentBlock(&types.ContentBlockMemberToolResult{Value: types.ToolResultBlock{
				ToolUseId: aws.String(m.ToolResult.ID),
				Content: []types.ToolResultContentBlock{
					&types.ToolResultContentBlockMemberText{Value: m.ToolResult.Content},
				},
				Status: toolResultStatus(m.ToolResult.IsError),
			}})
			// Bedrock requires every toolResult for one assistant turn's
			// parallel toolUse blocks to land in a SINGLE following user
			// turn — one user turn per tool result (the normalized
			// message shape agent.go builds) is invalid and Converse
			// rejects it with "Expected toolResult blocks ... for the
			// following Ids". Merge consecutive tool messages together.
			if n := len(out); n > 0 && out[n-1].Role == types.ConversationRoleUser && isToolResultTurn(out[n-1]) {
				out[n-1].Content = append(out[n-1].Content, resultBlock)
				continue
			}
			out = append(out, types.Message{
				Role:    types.ConversationRoleUser,
				Content: []types.ContentBlock{resultBlock},
			})
		}
	}
	return out
}

// bedrockImageFormat maps an attachment MIME type to Converse's
// ImageFormat enum. Callers only ever pass one of the four MIME types
// the attachments store allows (image/png, image/jpeg, image/webp,
// image/gif); anything else falls back to png rather than sending an
// empty Format Converse would reject outright.
func bedrockImageFormat(mime string) types.ImageFormat {
	switch mime {
	case "image/jpeg":
		return types.ImageFormatJpeg
	case "image/webp":
		return types.ImageFormatWebp
	case "image/gif":
		return types.ImageFormatGif
	default:
		return types.ImageFormatPng
	}
}

// flattenToolCallText renders an assistant tool call as plain text for
// requests with no ToolConfig, where a toolUse block would be rejected.
func flattenToolCallText(tc ToolCall) string {
	input := tc.Input
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	return fmt.Sprintf("[tool call: %s(%s)]", tc.Name, string(input))
}

// flattenToolResultText renders a tool result as plain user text for
// requests with no ToolConfig, where a toolResult block would be
// rejected.
func flattenToolResultText(tr ToolResult) string {
	return fmt.Sprintf("[tool result for %s: %s]", tr.ID, tr.Content)
}

func toolResultStatus(isError bool) types.ToolResultStatus {
	if isError {
		return types.ToolResultStatusError
	}
	return types.ToolResultStatusSuccess
}

// isToolResultTurn reports whether a user turn is made entirely of
// tool results (as opposed to plain user text) — only these are safe
// to append more tool results onto.
func isToolResultTurn(msg types.Message) bool {
	for _, c := range msg.Content {
		if _, ok := c.(*types.ContentBlockMemberToolResult); !ok {
			return false
		}
	}
	return len(msg.Content) > 0
}

// converseSystem builds the system blocks. Nova models get a cache
// point after the system text: cache writes cost nothing on Nova, so
// this earns the read discount whenever the system prompt repeats
// (D-018 stable prefix) and costs nothing when it doesn't. Titan
// rejects cachePoint blocks, hence the model-family gate; prefixes
// shorter than Nova's caching minimum are ignored server-side, not
// errors.
func converseSystem(system, model string) []types.SystemContentBlock {
	if system == "" {
		return nil
	}
	blocks := []types.SystemContentBlock{
		&types.SystemContentBlockMemberText{Value: system},
	}
	if strings.Contains(model, "amazon.nova") {
		blocks = append(blocks, &types.SystemContentBlockMemberCachePoint{
			Value: types.CachePointBlock{Type: types.CachePointTypeDefault},
		})
	}
	return blocks
}

// converseTools maps tool definitions onto a Converse tool config;
// nil when the request offers no tools.
func converseTools(defs []ToolDef) *types.ToolConfiguration {
	if len(defs) == 0 {
		return nil
	}
	tools := make([]types.Tool, 0, len(defs))
	for _, t := range defs {
		tools = append(tools, &types.ToolMemberToolSpec{
			Value: types.ToolSpecification{
				Name:        aws.String(t.Name),
				Description: aws.String(t.Description),
				InputSchema: &types.ToolInputSchemaMemberJson{
					Value: documentFromJSON(sanitizeNovaSchema(t.InputSchema)),
				},
			},
		})
	}
	return &types.ToolConfiguration{Tools: tools}
}

// sanitizeNovaSchema strips top-level schema keys AWS's Nova
// tool-troubleshooting guide identifies as contributors to invalid
// ToolUse sequences: the top-level tool input schema object supports
// only "type", "properties", and "required" — fields like $schema,
// additionalProperties, and title are rejected mid-stream instead of
// at request time. This adapter serves only Amazon first-party models
// (Bedrock, D-037), so the sanitize runs unconditionally rather than
// gating on model family. Nested schema keys (e.g. additionalProperties
// inside a property's own subschema) are untouched — only the
// top-level object is filtered. Unparseable or empty input is returned
// as-is; documentFromJSON already handles that case.
func sanitizeNovaSchema(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return raw
	}
	const (
		keyType       = "type"
		keyProperties = "properties"
		keyRequired   = "required"
	)
	clean := make(map[string]any, 3)
	for _, k := range []string{keyType, keyProperties, keyRequired} {
		if v, ok := m[k]; ok {
			clean[k] = v
		}
	}
	out, err := json.Marshal(clean)
	if err != nil {
		return raw
	}
	return out
}

// documentFromJSON converts a raw JSON schema into the
// document.Interface ToolSpecification requires.
func documentFromJSON(raw json.RawMessage) document.Interface {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	return document.NewLazyDocument(v)
}

// Embed uses InvokeModel with Titan embedding models
// (amazon.titan-embed-text-v1/v2). v1 outputs 1536 dimensions —
// matching memoryd's vector(1536) schema; v2 tops out at 1024 and
// needs a schema migration first. Texts run sequentially; token usage
// aggregates across calls.
func (b *Bedrock) Embed(ctx context.Context, model string, texts []string) ([][]float32, *stream.Usage, error) {
	if len(texts) == 0 {
		return nil, nil, fmt.Errorf("bedrock embed: no texts provided")
	}
	client, err := b.lazyClient(ctx)
	if err != nil {
		return nil, nil, err
	}

	embeddings := make([][]float32, len(texts))
	var totalInputTokens int

	for i, text := range texts {
		body, err := json.Marshal(map[string]any{"inputText": text})
		if err != nil {
			return nil, nil, fmt.Errorf("bedrock embed: marshal input for %s: %w", model, err)
		}

		output, err := client.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
			Body:        body,
			ContentType: aws.String("application/json"),
			Accept:      aws.String("application/json"),
			ModelId:     aws.String(model),
		})
		if err != nil {
			return nil, nil, fmt.Errorf("bedrock embed: invoke %s: %w", model, err)
		}

		emb, tokens, err := parseTitanEmbedding(output.Body)
		if err != nil {
			return nil, nil, fmt.Errorf("bedrock embed: %s: %w", model, err)
		}
		embeddings[i] = emb
		totalInputTokens += tokens
	}

	return embeddings, &stream.Usage{InputTokens: totalInputTokens}, nil
}

// parseTitanEmbedding extracts the vector and input token count from a
// Titan embed response: {"embedding":[...],"inputTextTokenCount":N}.
func parseTitanEmbedding(body []byte) ([]float32, int, error) {
	var resp struct {
		Embedding           []float32 `json:"embedding"`
		InputTextTokenCount int       `json:"inputTextTokenCount"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, 0, fmt.Errorf("unmarshal response: %w", err)
	}
	if len(resp.Embedding) == 0 {
		return nil, 0, fmt.Errorf("no embedding in response")
	}
	return resp.Embedding, resp.InputTextTokenCount, nil
}

var (
	_ Provider = (*Bedrock)(nil)
	_ Embedder = (*Bedrock)(nil)
)
