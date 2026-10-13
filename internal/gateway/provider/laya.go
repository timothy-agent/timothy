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

// LayaConfig configures one laya sidecar (D-164): Convai's Laya
// decision model behind laya-serve's Jev-compatible API.
type LayaConfig struct {
	Name    string
	BaseURL string // e.g. http://laya:8000, no trailing slash
	APIKey  string // bearer, only when the sidecar sets LAYA_API_KEY
	Timeout time.Duration
}

// layaTimeout bounds one decision: a forward pass takes tens of
// milliseconds, so anything near a chat timeout only delays failover.
const layaTimeout = 30 * time.Second

// Laya answers fixed-choice questions on a laya-serve endpoint. It
// never chats: CapDecide is its only capability.
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
// Routing never offers a laya entry on a chat route (CapChat is not
// declared), so this only answers a direct caller.
func (l *Laya) Stream(context.Context, CompletionRequest) (<-chan stream.StreamEvent, error) {
	return nil, fmt.Errorf("laya: %s is a decision model and cannot chat", l.cfg.Name)
}

// layaQuestion is one entry of the request's questions map.
type layaQuestion struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria,omitempty"`
}

// layaRequest is the POST /v1/systemone body. The question id is
// fixed: one request carries one question.
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
		Probabilities map[string]float64 `json:"probabilities"`
	} `json:"answers"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// Decide implements Decider. A choice maps to Laya's choice question
// with the options as criteria; yesno and score both map to a noul
// question, whose answer is the probability that the question holds:
// yesno thresholds it at one half, score returns it as is. Laya's
// own score type is an ordinal scale over labelled levels, which the
// gateway's [0, 1] score is not.
func (l *Laya) Decide(ctx context.Context, model string, req DecideRequest) (DecideResult, *stream.Usage, error) {
	body, err := json.Marshal(layaBody(model, req))
	if err != nil {
		return DecideResult{}, nil, fmt.Errorf("laya: marshal request: %w", err)
	}
	callCtx, cancel := context.WithTimeout(ctx, l.cfg.Timeout)
	defer cancel()
	resp, err := doWithRetry(callCtx, l.client, maxRetries, func() (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, l.cfg.BaseURL+"/v1/systemone", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if l.cfg.APIKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+l.cfg.APIKey)
		}
		return httpReq, nil
	}, nil)
	if err != nil {
		return DecideResult{}, nil, fmt.Errorf("laya: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var out layaResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return DecideResult{}, nil, fmt.Errorf("laya: decode response: %w", err)
	}
	usage := &stream.Usage{InputTokens: out.Usage.InputTokens, OutputTokens: out.Usage.OutputTokens}
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
		if ans.Noul == nil {
			return DecideResult{}, usage, fmt.Errorf("laya: reply carries no noul probability")
		}
		p := *ans.Noul
		choice := "no"
		if p >= 0.5 {
			choice = "yes"
		}
		return DecideResult{Choice: choice, Scores: map[string]float64{"yes": p, "no": 1 - p}}, usage, nil
	default: // DecideScore
		if ans.Noul == nil {
			return DecideResult{}, usage, fmt.Errorf("laya: reply carries no noul probability")
		}
		p := *ans.Noul
		return DecideResult{Score: &p}, usage, nil
	}
}

// layaBody maps a DecideRequest onto Laya's wire shape. The state is
// the context when the caller separated one, else the question itself
// (a missing state is a 400). A model naming a checkpoint is forwarded;
// empty leaves the sidecar's default in charge.
func layaBody(model string, req DecideRequest) layaRequest {
	q := layaQuestion{Type: "noul", Instructions: req.Question}
	if req.Kind == DecideChoice {
		q.Type = "choice"
		q.Criteria = make(map[string]string, len(req.Options))
		for _, o := range req.Options {
			desc := req.Descriptions[o]
			if strings.TrimSpace(desc) == "" {
				desc = o
			}
			q.Criteria[o] = desc
		}
	}
	state := req.Context
	if strings.TrimSpace(state) == "" {
		state = req.Question
	}
	return layaRequest{Model: model, State: state, Questions: map[string]layaQuestion{layaQuestionID: q}}
}

// ListModels implements ModelLister from GET /health, whose
// authenticated form lists the resident checkpoints. An empty list
// (not preloaded, or idle-unloaded) falls back to the one the sidecar
// image bakes.
func (l *Laya) ListModels(ctx context.Context) ([]AvailableModel, error) {
	resp, err := doWithRetry(ctx, l.client, maxRetries, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.cfg.BaseURL+"/health", nil)
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
