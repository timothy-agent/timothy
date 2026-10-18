package catalog

import (
	"context"
	"net/url"
	"strings"
)

// CandidateProviders maps a provider row's driver (+ base_url for
// openaicompat) to the litellm_provider value(s) its models could
// plausibly match against. Empty means no catalog vendor maps: listings
// and suggestions for the row return nothing, never the whole catalog
// (D-171).
func CandidateProviders(driver, baseURL string) []string {
	switch driver {
	case "anthropic":
		return []string{"anthropic"}
	case "bedrock":
		return []string{"bedrock", "bedrock_converse"}
	case "openaicompat", "openai-responses":
		return candidatesForHost(baseURL)
	case "jev":
		return []string{"typesafe", "jev"}
	default:
		return nil
	}
}

// CandidateProvidersForRow is CandidateProviders extended with admin-layer
// additions, relocated here so both admin and router can call it without an
// import cycle. First, options["litellm_provider"] (when set) always wins over
// the driver/host heuristic — an operator's explicit mapping beats inference,
// and "bedrock" still expands to the pair ["bedrock", "bedrock_converse"] like
// the heuristic does, since the catalog files Bedrock models under either
// key. Second, absent that override, a kind='cli' claude-cli row has no chat
// driver (D-051), but the CLI talks Anthropic's own API under the hood, so its
// candidate pool is "anthropic" rather than falling back to an unrestricted
// search. Third, when baseURL points to cliproxy (host cliproxy or port 8317),
// the proxy serves one of Claude or ChatGPT. Hints (provider name, default
// model, the model being priced) are scanned as tokens: only Claude signals
// → anthropic, only OpenAI signals → openai, both or neither → both sections.
// A single hint must not win just because it was listed first, and a
// substring like "gpt" inside an unrelated token must not count. Every other
// row defers to CandidateProviders as-is.
func CandidateProvidersForRow(kind, driver, baseURL string, opts map[string]string, hints ...string) []string {
	if lp := opts["litellm_provider"]; lp != "" {
		if lp == "bedrock" {
			return []string{"bedrock", "bedrock_converse"}
		}
		return []string{lp}
	}
	if kind == "cli" && driver == "claude-cli" {
		return []string{"anthropic"}
	}
	if isCliproxy(baseURL) {
		return cliproxyCandidates(hints)
	}
	return CandidateProviders(driver, baseURL)
}

// cliproxyCandidates picks the catalog section for a cliproxy row from
// hints. Tokens are split on anything that isn't a letter or digit, so
// "gpt-4o" yields "gpt" but "fingpt" does not.
func cliproxyCandidates(hints []string) []string {
	var anthropic, openai bool
	for _, h := range hints {
		for _, tok := range vendorTokens(h) {
			switch {
			case anthropicToken(tok):
				anthropic = true
			case openaiToken(tok):
				openai = true
			}
		}
	}
	switch {
	case anthropic && !openai:
		return []string{"anthropic"}
	case openai && !anthropic:
		return []string{"openai"}
	default:
		return []string{"anthropic", "openai"}
	}
}

// CliproxyServes reports whether a cliproxy row hinted by hints (name,
// default model) serves model id. The proxy lists Claude and OpenAI ids
// on every row; when hints signal one vendor only that vendor's ids pass
// (same token rule as cliproxyCandidates). A non-cliproxy baseURL or
// ambiguous hints keep every id (D-171).
func CliproxyServes(baseURL string, hints []string, id string) bool {
	if !isCliproxy(baseURL) {
		return true
	}
	row := cliproxyCandidates(hints)
	if len(row) != 1 {
		return true
	}
	own := cliproxyCandidates([]string{id})
	return len(own) == 1 && own[0] == row[0]
}

func vendorTokens(h string) []string {
	return strings.FieldsFunc(strings.ToLower(h), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
}

func anthropicToken(tok string) bool {
	switch tok {
	case "claude", "anthropic", "sonnet", "opus", "haiku":
		return true
	default:
		return strings.HasPrefix(tok, "claude")
	}
}

func openaiToken(tok string) bool {
	switch tok {
	case "openai", "chatgpt", "codex":
		return true
	}
	if strings.HasPrefix(tok, "gpt") {
		return true
	}
	// o-series ids (o1, o3, o4) share no "gpt" token. "o4-mini" splits
	// to "o4" + "mini", so the family is the two-character token.
	return len(tok) == 2 && tok[0] == 'o' && (tok[1] == '1' || tok[1] == '3' || tok[1] == '4')
}

func isCliproxy(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	return u.Hostname() == "cliproxy" || u.Port() == "8317"
}

// IsOpenAIHost reports whether baseURL points to official OpenAI (api.openai.com).
func IsOpenAIHost(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	return u.Hostname() == "api.openai.com"
}

func candidatesForHost(baseURL string) []string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil
	}
	host := u.Hostname()
	switch {
	case IsOpenAIHost(baseURL):
		return []string{"openai"}
	case host == "generativelanguage.googleapis.com":
		return []string{"gemini"}
	case strings.Contains(host, "z.ai"):
		return []string{"zai"}
	case host == "api.x.ai":
		return []string{"xai"}
	case host == "openrouter.ai":
		return []string{"openrouter"}
	case host == "localhost" || host == "127.0.0.1" || host == "host.docker.internal" || u.Port() == "11434":
		return []string{"ollama"}
	default:
		return nil // unmapped host: no catalog vendor (D-171)
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
// CandidateProviders or an admin-layer override of it). Empty
// candidates match nothing, so every suggestion comes back unmatched
// (D-171).
func (s *Store) Suggest(ctx context.Context, candidates []string, modelIDs []string) ([]Suggestion, error) {
	pool, err := s.byProvider(ctx, candidates)
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
