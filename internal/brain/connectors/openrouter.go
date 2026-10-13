package connectors

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
)

// OpenRouter's PKCE flow (D-163, issue #1248) mints a user-owned API
// key with no app registration: the browser authorizes on
// openrouter.ai, the callback carries a code, and brain trades the
// code plus verifier for the key server-side. The key goes straight
// into the secret store and a provider row names its ref; the browser
// never sees it.
const (
	openRouterAuthURL = "https://openrouter.ai/auth"
	openRouterKeysURL = "https://openrouter.ai/api/v1/auth/keys"
	openRouterBaseURL = "https://openrouter.ai/api/v1"
	openRouterName    = "OpenRouter"
	openRouterRef     = "OPENROUTER_API_KEY"
	// OpenRouterDefaultModel is a tool-capable :free model; the
	// operator can change it on the row afterwards.
	OpenRouterDefaultModel = "nvidia/nemotron-3-super-120b-a12b:free"
)

// OpenRouterReturn names where the browser lands after the callback.
const (
	OpenRouterReturnWizard   = "wizard"
	OpenRouterReturnSettings = "settings"
)

// ProviderAdmin is the slice of gwclient.Client the OpenRouter engine
// needs to pick unused names and create the row.
type ProviderAdmin interface {
	ListProviders(ctx context.Context) ([]gwclient.AdminProvider, error)
	ListSecrets(ctx context.Context) ([]gwclient.SecretRef, error)
	CreateProvider(ctx context.Context, p gwclient.AdminProvider) (string, error)
}

// OpenRouterAuth runs the PKCE dance. It shares the connectors'
// callback route, so HasState tells the dispatcher which engine owns a
// state token.
type OpenRouterAuth struct {
	Secrets   SecretRW
	Providers ProviderAdmin
	Client    *http.Client
	PublicURL string
	Log       *slog.Logger
	// AuthURL and KeysURL are the live endpoints; tests point them at a fake.
	AuthURL string
	KeysURL string

	mu     sync.Mutex
	states map[string]openRouterState
}

type openRouterState struct {
	verifier string
	returnTo string
	expires  time.Time
}

// OpenRouterConnected is a finished callback: the created row and
// where the browser should go.
type OpenRouterConnected struct {
	ProviderID string
	Name       string
	ReturnTo   string
}

func NewOpenRouterAuth(secrets SecretRW, providers ProviderAdmin, publicURL string, client *http.Client, log *slog.Logger) *OpenRouterAuth {
	return &OpenRouterAuth{
		Secrets:   secrets,
		Providers: providers,
		Client:    client,
		PublicURL: publicURL,
		Log:       log,
		AuthURL:   openRouterAuthURL,
		KeysURL:   openRouterKeysURL,
		states:    map[string]openRouterState{},
	}
}

// RedirectURI is the callback route shared with the connector engines.
func (a *OpenRouterAuth) RedirectURI() string {
	return strings.TrimRight(a.PublicURL, "/") + "/v1/connectors/oauth/callback"
}

// StartAuth returns the authorize URL for the browser. returnTo must
// be one of the OpenRouterReturn values.
func (a *OpenRouterAuth) StartAuth(returnTo string) (string, error) {
	if a.PublicURL == "" {
		return "", fmt.Errorf("TIMOTHY_PUBLIC_URL is not set; the OpenRouter redirect needs Timothy's public address: %w", ErrUnsupported)
	}
	if returnTo != OpenRouterReturnWizard && returnTo != OpenRouterReturnSettings {
		return "", fmt.Errorf("return_to must be %q or %q: %w", OpenRouterReturnWizard, OpenRouterReturnSettings, ErrInvalid)
	}
	verifier, err := randomURLToken(32)
	if err != nil {
		return "", fmt.Errorf("pkce verifier: %w", err)
	}
	state, err := randomURLToken(24)
	if err != nil {
		return "", fmt.Errorf("oauth state: %w", err)
	}
	now := time.Now()
	a.mu.Lock()
	for k, v := range a.states {
		if now.After(v.expires) {
			delete(a.states, k)
		}
	}
	a.states[state] = openRouterState{verifier: verifier, returnTo: returnTo, expires: now.Add(oauthStateTTL)}
	a.mu.Unlock()

	u, err := url.Parse(a.AuthURL)
	if err != nil {
		return "", fmt.Errorf("openrouter auth url: %w", err)
	}
	// OpenRouter passes only the callback_url through; sibling params
	// never reach the redirect. The state rides inside the callback's
	// own query, the only place it survives the roundtrip.
	cb, err := url.Parse(a.RedirectURI())
	if err != nil {
		return "", fmt.Errorf("openrouter callback url: %w", err)
	}
	cbq := cb.Query()
	cbq.Set("state", state)
	cb.RawQuery = cbq.Encode()
	q := u.Query()
	q.Set("callback_url", cb.String())
	q.Set("code_challenge", pkceChallenge(verifier))
	q.Set("code_challenge_method", "S256")
	q.Set("key_label", "Timothy")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// HasState reports whether this engine started the dance for state.
func (a *OpenRouterAuth) HasState(state string) bool {
	if a == nil || state == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.states[state]
	return ok
}

// Cancel consumes state after the user denied access and reports where
// the browser should land. ok is false for an unknown state.
func (a *OpenRouterAuth) Cancel(state string) (returnTo string, ok bool) {
	if a == nil || state == "" {
		return "", false
	}
	st, ok := a.pop(state)
	return st.returnTo, ok
}

func (a *OpenRouterAuth) pop(state string) (openRouterState, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	st, ok := a.states[state]
	delete(a.states, state)
	if !ok || time.Now().After(st.expires) {
		return openRouterState{}, false
	}
	return st, true
}

// HandleCallback trades the code for a key, stores it and creates the
// provider row. States are single-use. On failure the returned value
// still carries ReturnTo so the browser lands where it started.
func (a *OpenRouterAuth) HandleCallback(ctx context.Context, state, code string) (OpenRouterConnected, error) {
	st, ok := a.pop(state)
	if !ok {
		return OpenRouterConnected{}, fmt.Errorf("unknown or expired oauth state; start the OpenRouter connection again")
	}
	failed := OpenRouterConnected{ReturnTo: st.returnTo}
	if code == "" {
		return failed, fmt.Errorf("openrouter returned no code")
	}
	key, err := a.exchange(ctx, code, st.verifier)
	if err != nil {
		return failed, fmt.Errorf("openrouter key exchange: %w", err)
	}
	ref, name, err := a.freeNames(ctx)
	if err != nil {
		return failed, err
	}
	if err := a.Secrets.Set(ctx, ref, key); err != nil {
		return failed, fmt.Errorf("store openrouter key: %w", err)
	}
	id, err := a.Providers.CreateProvider(ctx, gwclient.AdminProvider{
		Name:          name,
		Kind:          "api",
		Driver:        "openaicompat",
		BaseURL:       openRouterBaseURL,
		DefaultModel:  OpenRouterDefaultModel,
		CredentialRef: ref,
		// App attribution: lists Timothy on OpenRouter's app rankings.
		Headers: map[string]string{
			"HTTP-Referer":       "https://github.com/timothy-agent/timothy",
			"X-OpenRouter-Title": "Timothy",
		},
		Enabled: true,
	})
	if err != nil {
		return failed, fmt.Errorf("create openrouter provider: %w", err)
	}
	return OpenRouterConnected{ProviderID: id, Name: name, ReturnTo: st.returnTo}, nil
}

// exchange posts the code and verifier and returns the minted key.
func (a *OpenRouterAuth) exchange(ctx context.Context, code, verifier string) (string, error) {
	body, _ := json.Marshal(map[string]string{
		"code":                  code,
		"code_verifier":         verifier,
		"code_challenge_method": "S256",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.KeysURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := a.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		if e.Error.Message != "" {
			return "", fmt.Errorf("http %d: %s", resp.StatusCode, e.Error.Message)
		}
		return "", fmt.Errorf("http %d", resp.StatusCode)
	}
	var out struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	if strings.TrimSpace(out.Key) == "" {
		return "", fmt.Errorf("response carried no key")
	}
	return out.Key, nil
}

// freeNames picks a credential ref no stored secret uses and a
// provider name no row uses, suffixing _2, _3 / " 2", " 3" as needed.
// The secret store is an upsert, so an unchecked ref would silently
// replace another provider's key.
func (a *OpenRouterAuth) freeNames(ctx context.Context) (ref, name string, err error) {
	refs, err := a.Providers.ListSecrets(ctx)
	if err != nil {
		return "", "", fmt.Errorf("list secret refs: %w", err)
	}
	rows, err := a.Providers.ListProviders(ctx)
	if err != nil {
		return "", "", fmt.Errorf("list providers: %w", err)
	}
	takenRefs := map[string]bool{}
	for _, r := range refs {
		takenRefs[r.RefName] = true
	}
	takenNames := map[string]bool{}
	for _, p := range rows {
		takenNames[strings.ToLower(p.Name)] = true
		if p.CredentialRef != "" {
			takenRefs[p.CredentialRef] = true
		}
	}
	ref = openRouterRef
	for i := 2; takenRefs[ref]; i++ {
		ref = fmt.Sprintf("%s_%d", openRouterRef, i)
	}
	name = openRouterName
	for i := 2; takenNames[strings.ToLower(name)]; i++ {
		name = fmt.Sprintf("%s %d", openRouterName, i)
	}
	return ref, name, nil
}
