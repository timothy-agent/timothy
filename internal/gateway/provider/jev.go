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

// DefaultJevModel is the default TypeSafe Jev decision model (D-163).
const DefaultJevModel = "jev-latest"

// DefaultJevBaseURL is the default TypeSafe API endpoint base.
const DefaultJevBaseURL = "https://api.typesafe.ai"

// JevConfig configures one TypeSafe Jev decision-model provider instance.
type JevConfig struct {
	Name    string
	BaseURL string // e.g. https://api.typesafe.ai — defaults to DefaultJevBaseURL
	APIKey  string // bearer token resolved from credential_ref
	Headers map[string]string
	Timeout time.Duration
}

const jevTimeout = 30 * time.Second

// Jev answers fixed-choice questions via TypeSafe's Jev API.
type Jev struct {
	cfg    JevConfig
	client *http.Client
}

// NewJev builds the driver; it performs no I/O.
func NewJev(cfg JevConfig) *Jev {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultJevBaseURL
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = jevTimeout
	}
	return &Jev{cfg: cfg, client: &http.Client{}}
}

func (j *Jev) Name() string               { return j.cfg.Name }
func (j *Jev) Kind() Kind                 { return KindAPI }
func (j *Jev) Capabilities() []Capability { return []Capability{CapDecide} }

// Stream always fails: a decision model has no completion to stream.
func (j *Jev) Stream(context.Context, CompletionRequest) (<-chan stream.StreamEvent, error) {
	return nil, fmt.Errorf("jev: %s is a decision model and cannot chat", j.cfg.Name)
}

// jevQuestion is one entry of the request's questions map.
type jevQuestion struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

// jevRequest is the POST /v1/systemone body.
type jevRequest struct {
	Model     string                 `json:"model,omitempty"`
	State     string                 `json:"state"`
	Questions map[string]jevQuestion `json:"questions"`
}

const jevQuestionID = "q"

// jevResponse is the part of the reply the driver reads.
type jevResponse struct {
	Model   string `json:"model,omitempty"`
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

func (j *Jev) systemoneEndpoint() string {
	endpoint := strings.TrimRight(j.cfg.BaseURL, "/")
	if !strings.HasSuffix(endpoint, "/v1/systemone") {
		endpoint = strings.TrimSuffix(endpoint, "/v1") + "/v1/systemone"
	}
	return endpoint
}

// Decide evaluates a DecideRequest via TypeSafe Jev's /v1/systemone endpoint.
func (j *Jev) Decide(ctx context.Context, model string, req DecideRequest) (DecideResult, *stream.Usage, error) {
	if model == "" {
		model = DefaultJevModel
	}
	body, err := json.Marshal(jevBody(model, req))
	if err != nil {
		return DecideResult{}, nil, fmt.Errorf("jev: marshal request: %w", err)
	}

	callCtx, cancel := context.WithTimeout(ctx, j.cfg.Timeout)
	defer cancel()

	endpoint := j.systemoneEndpoint()
	resp, err := doWithRetry(callCtx, j.client, maxRetries, func() (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if j.cfg.APIKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+j.cfg.APIKey)
		}
		for k, v := range j.cfg.Headers {
			httpReq.Header.Set(k, v)
		}
		return httpReq, nil
	}, nil)
	if err != nil {
		return DecideResult{}, nil, fmt.Errorf("jev: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return DecideResult{}, nil, fmt.Errorf("jev: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var out jevResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return DecideResult{}, nil, fmt.Errorf("jev: decode response: %w", err)
	}

	usage := &stream.Usage{
		InputTokens:  out.Usage.InputTokens,
		OutputTokens: out.Usage.OutputTokens,
	}

	ans, ok := out.Answers[jevQuestionID]
	if !ok {
		return DecideResult{}, usage, fmt.Errorf("jev: reply carries no answer")
	}

	switch req.Kind {
	case DecideChoice:
		for _, o := range req.Options {
			id := o
			if cutID, _, ok := strings.Cut(o, ": "); ok {
				id = cutID
			}
			if strings.EqualFold(ans.Choice, id) || strings.EqualFold(ans.Choice, o) {
				return DecideResult{Choice: id, Scores: ans.Probabilities}, usage, nil
			}
		}
		return DecideResult{}, usage, fmt.Errorf("jev: choice %q is not an allowed option", ans.Choice)
	default: // DecideYesNo
		if ans.Noul != nil {
			p := *ans.Noul
			if p < 0 || p > 1 {
				return DecideResult{}, usage, fmt.Errorf("jev: noul probability %v is outside [0, 1]", p)
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
		return DecideResult{}, usage, fmt.Errorf("jev: reply carries no noul probability")
	}
}

func jevBody(model string, req DecideRequest) jevRequest {
	q := jevQuestion{Instructions: req.Question}
	switch req.Kind {
	case DecideChoice:
		q.Type = "choice"
		crit := make(map[string]string, len(req.Options))
		for _, o := range req.Options {
			if id, desc, ok := strings.Cut(o, ": "); ok {
				crit[id] = desc
			} else {
				crit[o] = o
			}
		}
		q.Criteria = crit
	default: // DecideYesNo
		q.Type = "noul"
	}
	state := req.State
	if state == "" {
		state = req.Question
	}
	return jevRequest{Model: model, State: state, Questions: map[string]jevQuestion{jevQuestionID: q}}
}

func (j *Jev) modelsEndpoint() string {
	endpoint := strings.TrimRight(j.cfg.BaseURL, "/")
	endpoint = strings.TrimSuffix(endpoint, "/v1/systemone")
	endpoint = strings.TrimSuffix(endpoint, "/v1")
	return endpoint + "/v1/models"
}

// listJevModels queries TypeSafe's GET /v1/models and parses its {"models": [{"name": "...", "description": "..."}]} envelope.
func (j *Jev) listJevModels(ctx context.Context) ([]AvailableModel, error) {
	resp, err := doWithRetry(ctx, j.client, maxRetries, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, j.modelsEndpoint(), nil)
		if err != nil {
			return nil, err
		}
		if j.cfg.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+j.cfg.APIKey)
		}
		for k, v := range j.cfg.Headers {
			req.Header.Set(k, v)
		}
		return req, nil
	}, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var out struct {
		Models []struct {
			Name        string `json:"name"`
			ID          string `json:"id"`
			Description string `json:"description"`
		} `json:"models"`
		Data []AvailableModel `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("jev: decode models: %w", err)
	}

	var res []AvailableModel
	for _, m := range out.Models {
		id := m.Name
		if id == "" {
			id = m.ID
		}
		if id != "" {
			res = append(res, AvailableModel{ID: id, DisplayName: m.Description})
		}
	}
	if len(res) == 0 && len(out.Data) > 0 {
		res = out.Data
	}
	return res, nil
}

// ListModels implements ModelLister via GET /v1/models, falling back to known models on error.
func (j *Jev) ListModels(ctx context.Context) ([]AvailableModel, error) {
	models, err := j.listJevModels(ctx)
	if err == nil && len(models) > 0 {
		return models, nil
	}
	return []AvailableModel{
		{ID: "jev-latest"},
		{ID: "jev-preview"},
		{ID: "jev-1.13.0"},
	}, nil
}
