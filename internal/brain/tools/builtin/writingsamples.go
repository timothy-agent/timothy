package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/SumonMSelim/timothy/internal/brain/kb"
	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

const (
	writingSamplesDefaultK = 3
	writingSamplesMaxK     = 5
	// writingSamplesTotalByteBudget bounds the whole rendered result,
	// not each sample: Bangla runes are 3 bytes each, so a per-sample
	// rune cap let two samples cross the tool result offload threshold
	// (loop.DefaultOffloadThreshold, 8KB) and the model never saw them
	// inline. Sized so the default k stays comfortably under that with
	// headroom for titles, dates, and separators.
	writingSamplesTotalByteBudget = 6000
	// bengaliShare is the fraction of letter runes in the Bengali block
	// above which a text counts as Bangla.
	bengaliShare = 0.3
)

var writingSamplesLanguages = map[string]bool{"bn": true, "en": true}

// WritingSamplesCollectionFunc resolves the name of the kb collection
// holding the operator's own writing, "" when unset.
type WritingSamplesCollectionFunc func(ctx context.Context) string

// WritingSamplesFetchFunc returns the newest ready documents in a
// collection as plain text; main curries kb.Store.RecentDocumentTexts
// in.
type WritingSamplesFetchFunc func(ctx context.Context, name string, limit int) ([]kb.DocumentText, error)

type writingSamplesArgs struct {
	Language string `json:"language"`
	K        *int   `json:"k"`
}

// WritingSamples returns the operator's own writing by language rather
// than by topic: search_kb ranks on topic similarity, so an off-topic
// style sample never surfaces there.
func WritingSamples(collection WritingSamplesCollectionFunc, fetch WritingSamplesFetchFunc) *tools.Tool {
	return &tools.Tool{
		Name: "writing_samples",
		Description: `Returns pieces the owner wrote themselves, selected by language.

Use before drafting or rewriting any prose the owner will send or
publish, so the result sounds like them. Pass the language of the piece
you are about to write.

The samples are about unrelated topics on purpose: they are here for
voice, not for content. Copy sentence length, tone, how they address
the reader, and their punctuation habits. Never copy their subject
matter, facts, or sentences into what you write.

Do not use this to look up facts. search_kb is for facts.

Arguments:
- language (string, optional): "bn" for Bangla or "en" for English;
  omit to get the most recent samples in any language.
- k (integer, optional): how many samples to return, 1-5, default 3.

Returns numbered samples, each with its title, the date it was added,
and its text (long pieces are cut).`,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"language": {
					"type": "string",
					"enum": ["bn", "en"],
					"description": "Language of the piece being written; omit for any language."
				},
				"k": {
					"type": "integer",
					"minimum": 1,
					"maximum": 5,
					"description": "Number of samples to return; defaults to 3."
				}
			},
			"additionalProperties": false
		}`),
		Execute: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args writingSamplesArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &args); err != nil {
					return "", fmt.Errorf("writing_samples: invalid arguments: %w", err)
				}
			}
			if args.Language != "" && !writingSamplesLanguages[args.Language] {
				return "", fmt.Errorf("writing_samples: language must be bn or en, got %q", args.Language)
			}
			k := writingSamplesDefaultK
			if args.K != nil {
				k = *args.K
				if k < 1 || k > writingSamplesMaxK {
					return "", fmt.Errorf("writing_samples: k must be between 1 and %d, got %d", writingSamplesMaxK, k)
				}
			}
			name := collection(ctx)
			if name == "" {
				return "No writing samples collection is configured.", nil
			}
			docs, err := fetch(ctx, name, k)
			if err != nil {
				return "", fmt.Errorf("writing_samples: %w", err)
			}
			matched := make([]kb.DocumentText, 0, k)
			for _, d := range docs {
				if args.Language != "" && detectLanguage(d.Text) != args.Language {
					continue
				}
				matched = append(matched, d)
				if len(matched) == k {
					break
				}
			}
			if len(matched) == 0 {
				if args.Language == "" {
					return "No samples found in " + name + ".", nil
				}
				return "No " + args.Language + " samples found in " + name + ".", nil
			}
			return formatWritingSamples(matched), nil
		},
	}
}

func formatWritingSamples(docs []kb.DocumentText) string {
	perSample := writingSamplesTotalByteBudget / len(docs)
	var b strings.Builder
	for i, d := range docs {
		if i > 0 {
			b.WriteString("\n---\n\n")
		}
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteString(". ")
		b.WriteString(d.Title)
		b.WriteString("\n")
		b.WriteString(d.CreatedAt.Format("2006-01-02"))
		b.WriteString("\n\n")
		b.WriteString(capBytes(d.Text, perSample))
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

// capBytes cuts s to at most n bytes, backing off to the nearest rune
// boundary so a multi-byte character is never split, and marks a cut
// with " ...". n <= 0 returns "".
func capBytes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + " ..."
}

// detectLanguage reports "bn" when Bengali-block runes make up at
// least bengaliShare of the letters, "en" otherwise.
func detectLanguage(s string) string {
	var letters, bengali int
	for _, r := range s {
		if !unicode.IsLetter(r) {
			continue
		}
		letters++
		if r >= 0x0980 && r <= 0x09FF {
			bengali++
		}
	}
	if letters > 0 && float64(bengali)/float64(letters) >= bengaliShare {
		return "bn"
	}
	return "en"
}
