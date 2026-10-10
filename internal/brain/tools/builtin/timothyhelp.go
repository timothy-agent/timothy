package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

const (
	timothyHelpDefaultK = 6
	timothyHelpMaxK     = 10
	// timothyHelpExcerptMax caps each passage, in bytes.
	timothyHelpExcerptMax = 900
	// timothyHelpMaxBytes caps the whole result below
	// tools.DefaultOffloadThreshold, so it always inlines.
	timothyHelpMaxBytes = 7000
)

// HelpHit is one passage from Timothy's own docs.
type HelpHit struct {
	Title    string
	Excerpt  string
	DocsURL  string
	AppPath  string
	AppLabel string
}

// HelpAccount is one configured connector or channel row.
type HelpAccount struct {
	Kind    string
	Enabled bool
}

// TimothyHelpConfig wires timothy_help. Search is bound in main to the
// timothy-docs collection; every live field is optional and a nil one
// renders as unknown.
type TimothyHelpConfig struct {
	Search     func(ctx context.Context, query string, k int) ([]HelpHit, error)
	Version    string
	Features   func(ctx context.Context) map[string]bool
	Connectors func(ctx context.Context) ([]HelpAccount, error)
	Channels   func(ctx context.Context) ([]HelpAccount, error)
	Agents     func(ctx context.Context) ([]string, error)
	Workflows  bool
	Sandbox    func(ctx context.Context) error
}

type timothyHelpArgs struct {
	Query string `json:"query"`
	K     *int   `json:"k"`
}

// TimothyHelp answers questions about Timothy itself from the docs
// bundled with this build, plus a live block read from this instance.
// No model call.
func TimothyHelp(cfg TimothyHelpConfig) *tools.Tool {
	return &tools.Tool{
		Name: "timothy_help",
		Description: `Searches Timothy's own documentation for this version and reports this instance's live state.

Use for any question about Timothy itself: what it can do, how to set
something up, where a setting lives, which version is running, what is
configured.

Arguments:
- query (string, required): what the user wants to know about Timothy.
- k (integer, optional): how many passages to return, 1-10, default 6.

Returns a live instance block (version, feature switches, connectors,
channels, agents, workflows, sandbox) and numbered doc passages, each
with its page title, an app screen path and a docs URL when the page has
them. Link app screens as relative markdown links, e.g.
[Features](/settings/features), and docs as absolute URLs. Never invent
a path the result does not give.`,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query": {
					"type": "string",
					"description": "What the user wants to know about Timothy."
				},
				"k": {
					"type": "integer",
					"minimum": 1,
					"maximum": 10,
					"description": "Number of passages to return; defaults to 6."
				}
			},
			"required": ["query"],
			"additionalProperties": false
		}`),
		Trusted: true,
		Execute: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args timothyHelpArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("timothy_help: invalid arguments: %w", err)
			}
			if strings.TrimSpace(args.Query) == "" {
				return "", fmt.Errorf("timothy_help: query must not be empty")
			}
			k := timothyHelpDefaultK
			if args.K != nil {
				k = *args.K
				if k < 1 || k > timothyHelpMaxK {
					return "", fmt.Errorf("timothy_help: k must be between 1 and %d, got %d", timothyHelpMaxK, k)
				}
			}
			if cfg.Search == nil {
				return "", fmt.Errorf("timothy_help: docs search is not configured")
			}
			hits, err := cfg.Search(ctx, args.Query, k)
			if err != nil {
				return "", fmt.Errorf("timothy_help: %w", err)
			}
			return capBytes(cfg.live(ctx)+"\n\n"+formatHelpHits(hits), timothyHelpMaxBytes), nil
		},
	}
}

func (cfg TimothyHelpConfig) live(ctx context.Context) string {
	var b strings.Builder
	b.WriteString("This Timothy instance (live):\n")
	version := cfg.Version
	if version == "" {
		version = "unknown (local build)"
	}
	fmt.Fprintf(&b, "- Version: %s\n", version)

	if cfg.Features == nil {
		b.WriteString("- Feature switches: unknown\n")
	} else {
		var on, off []string
		for key, enabled := range cfg.Features(ctx) {
			if enabled {
				on = append(on, key)
			} else {
				off = append(off, key)
			}
		}
		slices.Sort(on)
		slices.Sort(off)
		fmt.Fprintf(&b, "- Feature switches on: %s\n", listOrNone(on))
		fmt.Fprintf(&b, "- Feature switches off: %s\n", listOrNone(off))
	}

	fmt.Fprintf(&b, "- Connectors: %s\n", accounts(ctx, cfg.Connectors))
	fmt.Fprintf(&b, "- Channels: %s\n", accounts(ctx, cfg.Channels))

	if cfg.Agents == nil {
		b.WriteString("- Agents: unknown\n")
	} else if names, err := cfg.Agents(ctx); err != nil {
		b.WriteString("- Agents: unknown\n")
	} else {
		fmt.Fprintf(&b, "- Agents: %s\n", listOrNone(names))
	}

	if cfg.Workflows {
		b.WriteString("- Workflows: enabled\n")
	} else {
		b.WriteString("- Workflows: disabled\n")
	}

	switch {
	case cfg.Sandbox == nil:
		b.WriteString("- Sandbox: unknown")
	case cfg.Sandbox(ctx) != nil:
		b.WriteString("- Sandbox: unavailable")
	default:
		b.WriteString("- Sandbox: available")
	}
	return b.String()
}

// accounts renders rows grouped by kind, sorted, with disabled ones
// counted separately.
func accounts(ctx context.Context, fn func(context.Context) ([]HelpAccount, error)) string {
	if fn == nil {
		return "unknown"
	}
	rows, err := fn(ctx)
	if err != nil {
		return "unknown"
	}
	if len(rows) == 0 {
		return "none configured"
	}
	total, disabled := map[string]int{}, map[string]int{}
	for _, r := range rows {
		total[r.Kind]++
		if !r.Enabled {
			disabled[r.Kind]++
		}
	}
	kinds := make([]string, 0, len(total))
	for kind := range total {
		kinds = append(kinds, kind)
	}
	slices.Sort(kinds)
	parts := make([]string, len(kinds))
	for i, kind := range kinds {
		s := kind + " (" + strconv.Itoa(total[kind]) + " account"
		if total[kind] != 1 {
			s += "s"
		}
		if n := disabled[kind]; n > 0 {
			s += ", " + strconv.Itoa(n) + " disabled"
		}
		parts[i] = s + ")"
	}
	return strings.Join(parts, ", ")
}

func listOrNone(items []string) string {
	if len(items) == 0 {
		return "none"
	}
	return strings.Join(items, ", ")
}

func formatHelpHits(hits []HelpHit) string {
	if len(hits) == 0 {
		return "No matching passages in Timothy's docs."
	}
	var b strings.Builder
	b.WriteString("Docs passages:\n")
	for i, h := range hits {
		fmt.Fprintf(&b, "%d. %s\n", i+1, h.Title)
		if h.AppPath != "" {
			label := h.AppLabel
			if label == "" {
				label = h.Title
			}
			fmt.Fprintf(&b, "App screen: [%s](%s)\n", label, h.AppPath)
		}
		if h.DocsURL != "" {
			fmt.Fprintf(&b, "Docs: %s\n", h.DocsURL)
		}
		b.WriteString(capBytes(strings.TrimSpace(h.Excerpt), timothyHelpExcerptMax))
		b.WriteString("\n\n")
	}
	return strings.TrimSpace(b.String())
}
