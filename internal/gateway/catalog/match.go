package catalog

import (
	"context"
	"net/url"
	"strings"
)

// CandidateProviders maps a provider row's driver (+ base_url for
// openaicompat) to the litellm_provider value(s) its models could
// plausibly match against. Empty slice means "no restriction": try
// every catalog provider.
func CandidateProviders(driver, baseURL string) []string {
	switch driver {
	case "anthropic":
		return []string{"anthropic"}
	case "bedrock":
		return []string{"bedrock", "bedrock_converse"}
	case "openaicompat", "openai-responses":
		return candidatesForHost(baseURL)
	default:
		return nil
	}
}

// CandidateProvidersForRow is CandidateProviders extended with two
// admin-layer additions, relocated here so both admin and router can
// call it without an import cycle. First, options["litellm_provider"]
// (when set) always wins over the driver/host heuristic — an
// operator's explicit mapping beats inference, and "bedrock" still
// expands to the pair ["bedrock", "bedrock_converse"] like the
// heuristic does, since the catalog files Bedrock models under either
// key. Second, absent that override, a kind='cli' claude-cli row has
// no chat driver (D-051), but the CLI talks Anthropic's own API under
// the hood, so its candidate pool is "anthropic" rather than falling
// back to an unrestricted search. Every other row (api-kind, or a cli
// driver other than claude-cli) defers to CandidateProviders as-is.
func CandidateProvidersForRow(kind, driver, baseURL string, opts map[string]string) []string {
	if lp := opts["litellm_provider"]; lp != "" {
		if lp == "bedrock" {
			return []string{"bedrock", "bedrock_converse"}
		}
		return []string{lp}
	}
	if kind == "cli" && driver == "claude-cli" {
		return []string{"anthropic"}
	}
	return CandidateProviders(driver, baseURL)
}

func candidatesForHost(baseURL string) []string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil
	}
	host := u.Hostname()
	switch {
	case host == "api.openai.com":
		return []string{"openai"}
	case host == "generativelanguage.googleapis.com":
		return []string{"gemini"}
	case strings.Contains(host, "z.ai"):
		return []string{"zai"}
	case host == "api.x.ai":
		return []string{"xai"}
	case host == "localhost" || host == "127.0.0.1" || host == "host.docker.internal" || u.Port() == "11434":
		return []string{"ollama"}
	default:
		return nil // no restriction: try every catalog provider
	}
}

// Match finds model_key's exact match: first against the full key,
// then against the segment after the last "/" — first match wins, no
// fuzzy matching. Empty string means no match.
func Match(modelID string, pool []Model) string {
	for _, m := range pool {
		if m.ModelKey == modelID {
			return m.ModelKey
		}
	}
	for _, m := range pool {
		if segment(m.ModelKey) == modelID {
			return m.ModelKey
		}
	}
	return ""
}

func segment(modelKey string) string {
	if i := strings.LastIndex(modelKey, "/"); i >= 0 {
		return modelKey[i+1:]
	}
	return modelKey
}

// StripOwnPrefix strips modelKey's own litellmProvider prefix when the
// key literally starts with "<litellmProvider>/" — the id a provider's
// API actually accepts, since LiteLLM namespaces model_key by provider
// (zai/glm-4.5 -> glm-4.5) but a bare key (gpt-4o) or a key with a
// deeper remainder (fireworks_ai/accounts/fireworks/models/x ->
// accounts/fireworks/models/x) passes through unchanged past the one
// leading prefix. Unlike segment (Match's last-segment rule), this
// only ever strips the entry's own provider prefix, never any other
// segment.
func StripOwnPrefix(modelKey, litellmProvider string) string {
	prefix := litellmProvider + "/"
	if strings.HasPrefix(modelKey, prefix) {
		return modelKey[len(prefix):]
	}
	return modelKey
}

// Suggestion is one declared model's catalog match — nil match means
// unmatched.
type Suggestion struct {
	ModelID           string
	Match             string // catalog model_key, "" when unmatched
	MaxInputTokens    *int64
	MaxOutputTokens   *int64
	InputPerMTok      *float64
	OutputPerMTok     *float64
	CacheReadPerMTok  *float64
	CacheWritePerMTok *float64
}

// Suggest matches each of modelIDs against the catalog rows for
// candidates (a row's candidate litellm_provider(s), typically from
// CandidateProviders or an admin-layer override of it), falling back
// to the whole catalog when candidates is nil/empty (no restriction).
func (s *Store) Suggest(ctx context.Context, candidates []string, modelIDs []string) ([]Suggestion, error) {
	var pool []Model
	var err error
	if len(candidates) == 0 {
		pool, err = s.allProviders(ctx)
	} else {
		pool, err = s.byProvider(ctx, candidates)
	}
	if err != nil {
		return nil, err
	}

	byKey := make(map[string]Model, len(pool))
	for _, m := range pool {
		byKey[m.ModelKey] = m
	}

	out := make([]Suggestion, len(modelIDs))
	for i, id := range modelIDs {
		sg := Suggestion{ModelID: id}
		if key := Match(id, pool); key != "" {
			m := byKey[key]
			sg.Match = key
			sg.MaxInputTokens = m.MaxInputTokens
			sg.MaxOutputTokens = m.MaxOutputTokens
			sg.InputPerMTok = m.InputPerMTok
			sg.OutputPerMTok = m.OutputPerMTok
			sg.CacheReadPerMTok = m.CacheReadPerMTok
			sg.CacheWritePerMTok = m.CacheWritePerMTok
		}
		out[i] = sg
	}
	return out, nil
}
