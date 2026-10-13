package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type subscriptionAuthHandler struct {
	baseURL string
	secret  string
	client  *http.Client
	log     *slog.Logger
}

func normalizeSubscriptionProvider(p string) (string, error) {
	p = strings.ToLower(strings.TrimSpace(p))
	switch p {
	case "chatgpt", "codex", "openai":
		return "codex", nil
	case "claude", "anthropic":
		return "claude", nil
	default:
		return "", fmt.Errorf("unsupported subscription provider: %q (expected chatgpt or claude)", p)
	}
}

func (s *subscriptionAuthHandler) start(w http.ResponseWriter, r *http.Request) {
	if s.secret == "" || s.secret == "change-me" {
		jsonError(w, http.StatusServiceUnavailable, "service_unavailable", "CLIPROXY_MANAGEMENT_KEY is not configured")
		return
	}

	var body struct {
		Provider string `json:"provider"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", "body must be a JSON object with provider")
		return
	}
	cpaProvider, err := normalizeSubscriptionProvider(body.Provider)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	targetURL := fmt.Sprintf("%s/v8/management/oauth/auth-url?provider=%s&is_webui=true", strings.TrimRight(s.baseURL, "/"), url.QueryEscape(cpaProvider))
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, targetURL, nil)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	req.Header.Set("Authorization", "Bearer "+s.secret)

	resp, err := s.client.Do(req)
	if err != nil {
		s.log.Error("failed to contact cliproxy", "error", err)
		jsonError(w, http.StatusBadGateway, "bad_gateway", "failed to contact cliproxy: "+err.Error())
		return
	}
	s.forwardResponse(w, resp)
}

func (s *subscriptionAuthHandler) status(w http.ResponseWriter, r *http.Request) {
	if s.secret == "" || s.secret == "change-me" {
		jsonError(w, http.StatusServiceUnavailable, "service_unavailable", "CLIPROXY_MANAGEMENT_KEY is not configured")
		return
	}

	state := r.URL.Query().Get("state")
	if state == "" {
		jsonError(w, http.StatusBadRequest, "bad_request", "query parameter state is required")
		return
	}

	targetURL := fmt.Sprintf("%s/v8/management/oauth/status?state=%s", strings.TrimRight(s.baseURL, "/"), url.QueryEscape(state))
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, targetURL, nil) // #nosec G704 -- internal cliproxy endpoint
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	req.Header.Set("Authorization", "Bearer "+s.secret)

	resp, err := s.client.Do(req) // #nosec G704 -- internal cliproxy endpoint
	if err != nil {
		s.log.Error("failed to query cliproxy oauth status", "error", err)
		jsonError(w, http.StatusBadGateway, "bad_gateway", "failed to contact cliproxy: "+err.Error())
		return
	}
	s.forwardResponse(w, resp)
}

func (s *subscriptionAuthHandler) callback(w http.ResponseWriter, r *http.Request) {
	if s.secret == "" || s.secret == "change-me" {
		jsonError(w, http.StatusServiceUnavailable, "service_unavailable", "CLIPROXY_MANAGEMENT_KEY is not configured")
		return
	}

	var body struct {
		Provider    string `json:"provider"`
		RedirectURL string `json:"redirect_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", "body must be a JSON object with provider and redirect_url")
		return
	}
	cpaProvider, err := normalizeSubscriptionProvider(body.Provider)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if strings.TrimSpace(body.RedirectURL) == "" {
		jsonError(w, http.StatusBadRequest, "bad_request", "redirect_url is required")
		return
	}

	payload, err := json.Marshal(map[string]string{
		"provider":     cpaProvider,
		"redirect_url": body.RedirectURL,
	})
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	targetURL := fmt.Sprintf("%s/v8/management/oauth/callback", strings.TrimRight(s.baseURL, "/"))
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, bytes.NewReader(payload))
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	req.Header.Set("Authorization", "Bearer "+s.secret)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		s.log.Error("failed to post cliproxy oauth callback", "error", err)
		jsonError(w, http.StatusBadGateway, "bad_gateway", "failed to contact cliproxy: "+err.Error())
		return
	}
	s.forwardResponse(w, resp)
}

func (s *subscriptionAuthHandler) cancel(w http.ResponseWriter, r *http.Request) {
	if s.secret == "" || s.secret == "change-me" {
		jsonError(w, http.StatusServiceUnavailable, "service_unavailable", "CLIPROXY_MANAGEMENT_KEY is not configured")
		return
	}

	state := r.URL.Query().Get("state")
	if state == "" {
		jsonError(w, http.StatusBadRequest, "bad_request", "query parameter state is required")
		return
	}

	targetURL := fmt.Sprintf("%s/v8/management/oauth/session?state=%s", strings.TrimRight(s.baseURL, "/"), url.QueryEscape(state))
	req, err := http.NewRequestWithContext(r.Context(), http.MethodDelete, targetURL, nil) // #nosec G704 -- internal cliproxy endpoint
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	req.Header.Set("Authorization", "Bearer "+s.secret)

	resp, err := s.client.Do(req) // #nosec G704 -- internal cliproxy endpoint
	if err != nil {
		s.log.Error("failed to cancel cliproxy oauth session", "error", err)
		jsonError(w, http.StatusBadGateway, "bad_gateway", "failed to contact cliproxy: "+err.Error())
		return
	}
	s.forwardResponse(w, resp)
}

func (s *subscriptionAuthHandler) forwardResponse(w http.ResponseWriter, resp *http.Response) {
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		s.log.Error("cliproxy rejected management authentication", "status", resp.StatusCode)
		jsonError(w, http.StatusBadGateway, "bad_gateway", "cliproxy rejected management authentication")
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (a *API) registerSubscriptionAuth(handle func(pattern string, h http.Handler), cliproxyURL, cliproxyKey string) {
	if cliproxyURL == "" {
		cliproxyURL = "http://cliproxy:8317"
	}
	sub := &subscriptionAuthHandler{
		baseURL: cliproxyURL,
		secret:  cliproxyKey,
		client:  &http.Client{Timeout: 30 * time.Second},
		log:     a.log,
	}

	handle("POST /v1/providers/subscription/auth/start", a.auth(http.HandlerFunc(sub.start)))
	handle("GET /v1/providers/subscription/auth/status", a.auth(http.HandlerFunc(sub.status)))
	handle("POST /v1/providers/subscription/auth/callback", a.auth(http.HandlerFunc(sub.callback)))
	handle("DELETE /v1/providers/subscription/auth/session", a.auth(http.HandlerFunc(sub.cancel)))
}
