package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// DefaultLayaModel is the default English typed-decisions checkpoint (D-163).
const DefaultLayaModel = "typed-decisions"

// defaultScoreLevels provides a standard ordinal scale for score questions when
// the request does not provide explicit level descriptions.
var defaultScoreLevels = []string{
	"none: not at all present or relevant",
	"low: minor or slight presence",
	"medium: moderate or noticeable presence",
	"high: strong or critical presence",
}

// LayaConfig configures one Laya decision-model provider instance.
type LayaConfig struct {
	Name    string
	BaseURL string // e.g. http://laya:8000 — no trailing slash
	APIKey  string // optional bearer token
	Headers map[string]string
	Timeout time.Duration
}

// layaTimeout bounds one decision: a forward pass takes tens of
// milliseconds, so anything near a chat timeout only delays failover.
const layaTimeout = 30 * time.Second

// Laya answers fixed-choice questions on a laya-serve endpoint.
type Laya struct {
	cfg    LayaConfig
	client *http.Client
}

// NewLaya builds the driver; it performs no I/O.
func NewLaya(cfg LayaConfig) *Laya {
	if cfg.Timeout <= 0 {
		cfg.Timeout = layaTimeout
	}
	return &Laya{cfg: cfg, client: &http.Client{}}
}

func (l *Laya) Name() string               { return l.cfg.Name }
func (l *Laya) Kind() Kind                 { return KindAPI }
func (l *Laya) Capabilities() []Capability { return []Capability{CapDecide} }

// Stream always fails: a decision model has no completion to stream.
func (l *Laya) Stream(context.Context, CompletionRequest) (<-chan stream.StreamEvent, error) {
	return nil, fmt.Errorf("laya: %s is a decision model and cannot chat", l.cfg.Name)
}

// layaQuestion is one entry of the request's questions map.
type layaQuestion struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

// layaRequest is the POST /v1/systemone body.
type layaRequest struct {
	Model     string                  `json:"model,omitempty"`
	State     string                  `json:"state"`
	Questions map[string]layaQuestion `json:"questions"`
}

const layaQuestionID = "q"

// layaResponse is the part of the reply the driver reads.
type layaResponse struct {
	Answers map[string]struct {
		Type          string             `json:"type"`
		Choice        string             `json:"choice"`
		Noul          *float64           `json:"noul"`
		Score         *float64           `json:"score"`
		Probabilities map[string]float64 `json:"probabilities"`
	} `json:"answers"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func (l *Laya) systemoneEndpoint() string {
	endpoint := strings.TrimRight(l.cfg.BaseURL, "/")
	if !strings.HasSuffix(endpoint, "/v1/systemone") {
		endpoint = strings.TrimSuffix(endpoint, "/v1") + "/v1/systemone"
	}
	return endpoint
}

func (l *Laya) healthEndpoint() string {
	endpoint := strings.TrimRight(l.cfg.BaseURL, "/")
	endpoint = strings.TrimSuffix(endpoint, "/v1/systemone")
	endpoint = strings.TrimSuffix(endpoint, "/v1")
	return endpoint + "/health"
}

func scoreCriteria(req DecideRequest) []string {
	if len(req.Options) >= 2 {
		return req.Options
	}
	return defaultScoreLevels
}

// Decide evaluates a DecideRequest via Laya's /v1/systemone endpoint.
func (l *Laya) Decide(ctx context.Context, model string, req DecideRequest) (DecideResult, *stream.Usage, error) {
	body, err := json.Marshal(layaBody(model, req))
	if err != nil {
		return DecideResult{}, nil, fmt.Errorf("laya: marshal request: %w", err)
	}

	callCtx, cancel := context.WithTimeout(ctx, l.cfg.Timeout)
	defer cancel()

	endpoint := l.systemoneEndpoint()
	resp, err := doWithRetry(callCtx, l.client, maxRetries, func() (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if l.cfg.APIKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+l.cfg.APIKey)
		}
		for k, v := range l.cfg.Headers {
			httpReq.Header.Set(k, v)
		}
		return httpReq, nil
	}, nil)
	if err != nil {
		return DecideResult{}, nil, fmt.Errorf("laya: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return DecideResult{}, nil, fmt.Errorf("laya: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var out layaResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return DecideResult{}, nil, fmt.Errorf("laya: decode response: %w", err)
	}

	usage := &stream.Usage{
		InputTokens:  out.Usage.InputTokens,
		OutputTokens: out.Usage.OutputTokens,
	}

	ans, ok := out.Answers[layaQuestionID]
	if !ok {
		return DecideResult{}, usage, fmt.Errorf("laya: reply carries no answer")
	}

	switch req.Kind {
	case DecideChoice:
		for _, o := range req.Options {
			if ans.Choice == o {
				return DecideResult{Choice: o, Scores: ans.Probabilities}, usage, nil
			}
		}
		return DecideResult{}, usage, fmt.Errorf("laya: choice %q is not an allowed option", ans.Choice)
	case DecideYesNo:
		if ans.Noul != nil {
			p := *ans.Noul
			if p < 0 || p > 1 {
				return DecideResult{}, usage, fmt.Errorf("laya: noul probability %v is outside [0, 1]", p)
			}
			choice := "no"
			if p >= 0.5 {
				choice = "yes"
			}
			return DecideResult{Choice: choice, Scores: map[string]float64{"yes": p, "no": 1 - p}}, usage, nil
		}
		if ans.Choice != "" {
			for _, o := range []string{"yes", "no"} {
				if strings.EqualFold(ans.Choice, o) {
					return DecideResult{Choice: o, Scores: ans.Probabilities}, usage, nil
				}
			}
		}
		return DecideResult{}, usage, fmt.Errorf("laya: reply carries no noul probability")
	default: // DecideScore
		levels := scoreCriteria(req)
		maxIdx := float64(len(levels) - 1)
		if maxIdx <= 0 {
			maxIdx = 1.0
		}

		if ans.Score != nil {
			raw := *ans.Score
			if raw < 0 || raw > maxIdx {
				return DecideResult{}, usage, fmt.Errorf("laya: score %v is outside [0, %g]", raw, maxIdx)
			}
			norm := raw / maxIdx
			if norm < 0 {
				norm = 0
			} else if norm > 1 {
				norm = 1
			}
			return DecideResult{Score: &norm, Scores: ans.Probabilities}, usage, nil
		}
		return DecideResult{}, usage, fmt.Errorf("laya: reply carries no score")
	}
}

func layaBody(model string, req DecideRequest) layaRequest {
	q := layaQuestion{Instructions: req.Question}
	switch req.Kind {
	case DecideChoice:
		q.Type = "choice"
		crit := make(map[string]string, len(req.Options))
		for _, o := range req.Options {
			crit[o] = o
		}
		q.Criteria = crit
	case DecideScore:
		q.Type = "score"
		q.Criteria = scoreCriteria(req)
	default: // DecideYesNo
		q.Type = "noul"
	}
	state := req.Question
	return layaRequest{Model: model, State: state, Questions: map[string]layaQuestion{layaQuestionID: q}}
}

// ListModels implements ModelLister from GET /health.
func (l *Laya) ListModels(ctx context.Context) ([]AvailableModel, error) {
	resp, err := doWithRetry(ctx, l.client, maxRetries, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.healthEndpoint(), nil)
		if err != nil {
			return nil, err
		}
		if l.cfg.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+l.cfg.APIKey)
		}
		return req, nil
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("laya: list models: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var out struct {
		Loaded []string `json:"loaded"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("laya: decode health: %w", err)
	}
	if len(out.Loaded) == 0 {
		return []AvailableModel{{ID: "typed-decisions"}}, nil
	}
	models := make([]AvailableModel, 0, len(out.Loaded))
	for _, id := range out.Loaded {
		models = append(models, AvailableModel{ID: id})
	}
	return models, nil
}
