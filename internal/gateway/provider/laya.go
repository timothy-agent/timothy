package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// DefaultLayaModel is the default English typed-decisions checkpoint (D-163).
const DefaultLayaModel = "convaiinnovations/laya-typed-decisions"

// LayaConfig configures one Laya decision-model provider instance.
type LayaConfig struct {
	Name    string
	BaseURL string            // e.g. http://laya:8000 — no trailing slash
	Headers map[string]string // extra request headers
	Timeout time.Duration     // hard per-request timeout
}

// Laya answers fixed-choice questions via the Laya System One API (/v1/systemone).
type Laya struct {
	cfg    LayaConfig
	client *http.Client
}

// NewLaya builds the driver; it performs no I/O.
func NewLaya(cfg LayaConfig) *Laya {
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	return &Laya{cfg: cfg, client: &http.Client{}}
}

func (l *Laya) Name() string { return l.cfg.Name }
func (l *Laya) Kind() Kind   { return KindAPI }

func (l *Laya) Capabilities() []Capability {
	return []Capability{CapDecide}
}

// Stream is not supported on a dedicated decision-model provider.
func (l *Laya) Stream(context.Context, CompletionRequest) (<-chan stream.StreamEvent, error) {
	return nil, errors.New("laya: does not support streaming chat completions")
}

type layaRequest struct {
	Model     string                      `json:"model,omitempty"`
	State     string                      `json:"state,omitempty"`
	Questions map[string]layaQuestionSpec `json:"questions"`
}

type layaQuestionSpec struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

type layaResponse struct {
	Model   string                    `json:"model"`
	Answers map[string]layaAnswerSpec `json:"answers"`
	Usage   *layaUsage                `json:"usage,omitempty"`
}

type layaAnswerSpec struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice,omitempty"`
	Confidence    float64            `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Noul          *float64           `json:"noul,omitempty"`
	Score         *float64           `json:"score,omitempty"`
}

type layaUsage struct {
	InputTokens int `json:"input_tokens"`
}

// Decide evaluates a DecideRequest via Laya's /v1/systemone endpoint.
func (l *Laya) Decide(ctx context.Context, model string, req DecideRequest) (DecideResult, *stream.Usage, error) {
	if l.cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, l.cfg.Timeout)
		defer cancel()
	}

	if model == "" {
		model = DefaultLayaModel
	}

	qSpec := layaQuestionSpec{
		Instructions: req.Question,
	}

	switch req.Kind {
	case DecideChoice:
		qSpec.Type = "choice"
		criteria := make(map[string]string, len(req.Options))
		for _, opt := range req.Options {
			criteria[opt] = opt
		}
		qSpec.Criteria = criteria
	case DecideYesNo:
		qSpec.Type = "noul"
	case DecideScore:
		qSpec.Type = "score"
		qSpec.Criteria = []string{"0", "1"}
	default:
		return DecideResult{}, nil, fmt.Errorf("laya: unsupported question kind %q", req.Kind)
	}

	bodyBytes, err := json.Marshal(layaRequest{
		Model: model,
		State: req.Question,
		Questions: map[string]layaQuestionSpec{
			"q": qSpec,
		},
	})
	if err != nil {
		return DecideResult{}, nil, fmt.Errorf("laya: marshal request: %w", err)
	}

	endpoint := strings.TrimRight(l.cfg.BaseURL, "/")
	if !strings.HasSuffix(endpoint, "/v1/systemone") {
		endpoint = strings.TrimSuffix(endpoint, "/v1") + "/v1/systemone"
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return DecideResult{}, nil, fmt.Errorf("laya: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	for k, v := range l.cfg.Headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := l.client.Do(httpReq)
	if err != nil {
		return DecideResult{}, nil, fmt.Errorf("laya: do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return DecideResult{}, nil, fmt.Errorf("laya: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var layaResp layaResponse
	if err := json.NewDecoder(resp.Body).Decode(&layaResp); err != nil {
		return DecideResult{}, nil, fmt.Errorf("laya: decode response: %w", err)
	}

	ans, ok := layaResp.Answers["q"]
	if !ok {
		return DecideResult{}, nil, errors.New("laya: response missing answer for question")
	}

	var usage *stream.Usage
	if layaResp.Usage != nil {
		usage = &stream.Usage{
			InputTokens: layaResp.Usage.InputTokens,
		}
	}

	var res DecideResult
	switch req.Kind {
	case DecideChoice:
		if ans.Choice == "" {
			return DecideResult{}, usage, errors.New("laya: empty choice in reply")
		}
		res.Choice = ans.Choice
		res.Scores = ans.Probabilities
	case DecideYesNo:
		if ans.Noul != nil {
			p := *ans.Noul
			if p >= 0.5 {
				res.Choice = "yes"
			} else {
				res.Choice = "no"
			}
			res.Scores = map[string]float64{
				"yes": p,
				"no":  1.0 - p,
			}
		} else if ans.Choice != "" {
			res.Choice = strings.ToLower(ans.Choice)
			res.Scores = ans.Probabilities
		} else {
			return DecideResult{}, usage, errors.New("laya: missing noul probability in reply")
		}
	case DecideScore:
		if ans.Score != nil {
			score := *ans.Score
			res.Score = &score
			res.Scores = ans.Probabilities
		} else {
			return DecideResult{}, usage, errors.New("laya: missing score in reply")
		}
	}

	return res, usage, nil
}
