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

// defaultScoreLevels provides a standard ordinal scale for score questions when
// the request does not provide explicit level descriptions.
var defaultScoreLevels = []string{
	"none: not at all present or relevant",
	"low: minor or slight presence",
	"medium: moderate or noticeable presence",
	"high: strong or critical presence",
}

// SystemOneConfig configures one decision-model provider instance that
// speaks the POST /v1/systemone wire (laya and jev drivers, D-163).
type SystemOneConfig struct {
	Name    string
	BaseURL string // /v1 and /v1/systemone suffixes are accepted
	APIKey  string // optional bearer token
	Headers map[string]string
	Timeout time.Duration
}

// systemoneTimeout bounds one decision: a forward pass takes tens of
// milliseconds, so anything near a chat timeout only delays failover.
const systemoneTimeout = 30 * time.Second

// systemonePreset holds what differs between the drivers that share the
// /v1/systemone wire.
type systemonePreset struct {
	driver         string // driver name, prefixes every error
	defaultBaseURL string
	defaultModel   string // sent when the chain entry names no model; empty omits the field
	listModels     func(ctx context.Context, s *SystemOne) ([]AvailableModel, error)
}

// SystemOne answers fixed-choice questions on a /v1/systemone endpoint.
type SystemOne struct {
	cfg    SystemOneConfig
	preset systemonePreset
	client *http.Client
}

func newSystemOne(p systemonePreset, cfg SystemOneConfig) *SystemOne {
	if cfg.BaseURL == "" {
		cfg.BaseURL = p.defaultBaseURL
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = systemoneTimeout
	}
	return &SystemOne{cfg: cfg, preset: p, client: &http.Client{}}
}

func (s *SystemOne) Name() string               { return s.cfg.Name }
func (s *SystemOne) Kind() Kind                 { return KindAPI }
func (s *SystemOne) Capabilities() []Capability { return []Capability{CapDecide} }

// Stream always fails: a decision model has no completion to stream.
func (s *SystemOne) Stream(context.Context, CompletionRequest) (<-chan stream.StreamEvent, error) {
	return nil, fmt.Errorf("%s: %s is a decision model and cannot chat", s.preset.driver, s.cfg.Name)
}

// systemoneQuestion is one entry of the request's questions map.
type systemoneQuestion struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

// systemoneRequest is the POST /v1/systemone body.
type systemoneRequest struct {
	Model     string                       `json:"model,omitempty"`
	State     string                       `json:"state"`
	Questions map[string]systemoneQuestion `json:"questions"`
}

const systemoneQuestionID = "q"

// systemoneResponse is the part of the reply the driver reads.
type systemoneResponse struct {
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

func (s *SystemOne) systemoneEndpoint() string {
	endpoint := strings.TrimRight(s.cfg.BaseURL, "/")
	if !strings.HasSuffix(endpoint, "/v1/systemone") {
		endpoint = strings.TrimSuffix(endpoint, "/v1") + "/v1/systemone"
	}
	return endpoint
}

// rootURL is the base URL without a /v1 or /v1/systemone suffix.
func (s *SystemOne) rootURL() string {
	endpoint := strings.TrimRight(s.cfg.BaseURL, "/")
	endpoint = strings.TrimSuffix(endpoint, "/v1/systemone")
	return strings.TrimSuffix(endpoint, "/v1")
}

func (s *SystemOne) setHeaders(req *http.Request) {
	if s.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.cfg.APIKey)
	}
	for k, v := range s.cfg.Headers {
		req.Header.Set(k, v)
	}
}

func scoreCriteria(req DecideRequest) []string {
	if len(req.Options) >= 2 {
		return req.Options
	}
	return defaultScoreLevels
}

// Decide evaluates a DecideRequest via the /v1/systemone endpoint.
func (s *SystemOne) Decide(ctx context.Context, model string, req DecideRequest) (DecideResult, *stream.Usage, error) {
	name := s.preset.driver
	if model == "" {
		model = s.preset.defaultModel
	}
	body, err := json.Marshal(systemoneBody(model, req))
	if err != nil {
		return DecideResult{}, nil, fmt.Errorf("%s: marshal request: %w", name, err)
	}

	callCtx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()

	endpoint := s.systemoneEndpoint()
	resp, err := doWithRetry(callCtx, s.client, maxRetries, func() (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		s.setHeaders(httpReq)
		return httpReq, nil
	}, nil)
	if err != nil {
		return DecideResult{}, nil, fmt.Errorf("%s: %w", name, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return DecideResult{}, nil, fmt.Errorf("%s: HTTP %d: %s", name, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var out systemoneResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return DecideResult{}, nil, fmt.Errorf("%s: decode response: %w", name, err)
	}

	usage := &stream.Usage{
		InputTokens:  out.Usage.InputTokens,
		OutputTokens: out.Usage.OutputTokens,
	}

	ans, ok := out.Answers[systemoneQuestionID]
	if !ok {
		return DecideResult{}, usage, fmt.Errorf("%s: reply carries no answer", name)
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
		return DecideResult{}, usage, fmt.Errorf("%s: choice %q is not an allowed option", name, ans.Choice)
	case DecideYesNo:
		if ans.Noul != nil {
			p := *ans.Noul
			if p < 0 || p > 1 {
				return DecideResult{}, usage, fmt.Errorf("%s: noul probability %v is outside [0, 1]", name, p)
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
		return DecideResult{}, usage, fmt.Errorf("%s: reply carries no noul probability", name)
	default: // DecideScore
		levels := scoreCriteria(req)
		maxIdx := float64(len(levels) - 1)
		if maxIdx <= 0 {
			maxIdx = 1.0
		}

		if ans.Score != nil {
			raw := *ans.Score
			if raw < 0 || raw > maxIdx {
				return DecideResult{}, usage, fmt.Errorf("%s: score %v is outside [0, %g]", name, raw, maxIdx)
			}
			norm := raw / maxIdx
			if norm < 0 {
				norm = 0
			} else if norm > 1 {
				norm = 1
			}
			return DecideResult{Score: &norm, Scores: ans.Probabilities}, usage, nil
		}
		return DecideResult{}, usage, fmt.Errorf("%s: reply carries no score", name)
	}
}

func systemoneBody(model string, req DecideRequest) systemoneRequest {
	q := systemoneQuestion{Instructions: req.Question}
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
	case DecideScore:
		q.Type = "score"
		q.Criteria = scoreCriteria(req)
	default: // DecideYesNo
		q.Type = "noul"
	}
	state := req.State
	if state == "" {
		state = req.Question
	}
	return systemoneRequest{Model: model, State: state, Questions: map[string]systemoneQuestion{systemoneQuestionID: q}}
}

// ListModels implements ModelLister through the preset's lister.
func (s *SystemOne) ListModels(ctx context.Context) ([]AvailableModel, error) {
	models, err := s.preset.listModels(ctx, s)
	if err != nil {
		return nil, fmt.Errorf("%s: list models: %w", s.preset.driver, err)
	}
	return models, nil
}

// getJSON GETs url and decodes the reply into out.
func (s *SystemOne) getJSON(ctx context.Context, url string, out any) error {
	resp, err := doWithRetry(ctx, s.client, maxRetries, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		s.setHeaders(req)
		return req, nil
	}, nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return nil
}
