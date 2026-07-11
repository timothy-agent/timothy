package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
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
// Credentials come from the AWS SDK default chain (env vars, shared
// config, IAM role) — never from code or the providers table. For
// local development with SSO, set credential_ref to the profile name
// and run `aws sso login --profile <name>` first. When running on AWS
// with an IAM role, leave credential_ref empty: a profile name that
// has no matching shared config makes client construction fail.
//
// Providers table example:
//
//	driver = 'bedrock'
//	base_url = 'us-east-1'                       -- AWS region
//	default_model = 'us.amazon.nova-pro-v1:0'    -- inference-profile ID
//	credential_ref = '<aws-profile>'             -- local SSO only; empty on AWS
//
// Models must be enabled on the Bedrock console "Model access" page.
type BedrockConfig struct {
	Name    string
	Region  string // e.g. "us-east-1"; defaults to us-east-1
	Profile string // optional AWS profile name (from credential_ref)
	Timeout time.Duration
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

func (b *Bedrock) Capabilities() []Capability {
	return []Capability{CapChat, CapStreaming, CapTools, CapEmbeddings}
}

// lazyClient builds the AWS client on first use (default credential
// chain: env vars, shared config, IAM role). sync.Once makes it safe
// under concurrent Stream/Embed calls; a load failure is sticky, which
// is right — a bad profile or region never heals mid-process.
func (b *Bedrock) lazyClient(ctx context.Context) (*bedrockruntime.Client, error) {
	b.initOnce.Do(func() {
		opts := []func(*config.LoadOptions) error{
			config.WithRegion(b.cfg.Region),
		}
		if b.cfg.Profile != "" {
			opts = append(opts, config.WithSharedConfigProfile(b.cfg.Profile))
		}
		awsCfg, err := config.LoadDefaultConfig(ctx, opts...)
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

	// Nova models are served through inference profiles, so ModelId is
	// e.g. "us.amazon.nova-pro-v1:0" — the bare model ID rejects
	// on-demand invocation.
	input := &bedrockruntime.ConverseStreamInput{
		ModelId:    aws.String(req.Model),
		Messages:   converseMessages(req.Messages),
		ToolConfig: converseTools(req.Tools),
		System:     converseSystem(req.System, req.Model),
	}
	if req.MaxTokens > 0 {
		input.InferenceConfig = &types.InferenceConfiguration{
			MaxTokens: aws.Int32(int32(req.MaxTokens)), //nolint:gosec // token caps are far below int32 max
		}
	}

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

// converseMessages maps normalized messages onto Bedrock Converse
// messages. Tool results ride as user-role toolResult blocks;
// assistant messages that carry neither text nor tool calls are
// dropped — Converse rejects empty content blocks.
func converseMessages(msgs []Message) []types.Message {
	out := make([]types.Message, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case "user":
			out = append(out, types.Message{
				Role:    types.ConversationRoleUser,
				Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: m.Content}},
			})
		case "assistant":
			blocks := []types.ContentBlock{}
			if m.Content != "" {
				blocks = append(blocks, &types.ContentBlockMemberText{Value: m.Content})
			}
			for _, tc := range m.ToolCalls {
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
			resultBlock := types.ToolResultBlock{
				ToolUseId: aws.String(m.ToolResult.ID),
				Content: []types.ToolResultContentBlock{
					&types.ToolResultContentBlockMemberText{Value: m.ToolResult.Content},
				},
				Status: types.ToolResultStatusSuccess,
			}
			if m.ToolResult.IsError {
				resultBlock.Status = types.ToolResultStatusError
			}
			out = append(out, types.Message{
				Role:    types.ConversationRoleUser,
				Content: []types.ContentBlock{&types.ContentBlockMemberToolResult{Value: resultBlock}},
			})
		}
	}
	return out
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
					Value: documentFromJSON(t.InputSchema),
				},
			},
		})
	}
	return &types.ToolConfiguration{Tools: tools}
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
