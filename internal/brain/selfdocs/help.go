package selfdocs

import (
	"context"
	"fmt"

	"github.com/SumonMSelim/timothy/internal/brain/memclient"
	"github.com/SumonMSelim/timothy/internal/brain/tools/builtin"
)

// Searcher is memoryd's KB search; *memclient.Client satisfies it.
type Searcher interface {
	KBSearch(ctx context.Context, query string, collectionNames, boostCollections []string, mode string, k int) ([]memclient.KBChunkHit, error)
}

// MetaReader reads kb_documents.meta; *kb.Store satisfies it.
type MetaReader interface {
	DocumentMeta(ctx context.Context, ids []string) (map[string]map[string]string, error)
}

// HelpSearch returns timothy_help's search: hybrid, scoped to the
// collection name only, each hit joined with its page's app_path and
// app_label.
func HelpSearch(search Searcher, meta MetaReader, name string) func(ctx context.Context, query string, k int) ([]builtin.HelpHit, error) {
	return func(ctx context.Context, query string, k int) ([]builtin.HelpHit, error) {
		hits, err := search.KBSearch(ctx, query, []string{name}, nil, "hybrid", k)
		if err != nil {
			return nil, fmt.Errorf("selfdocs search: %w", err)
		}
		ids := make([]string, 0, len(hits))
		for _, h := range hits {
			ids = append(ids, h.DocumentID)
		}
		metas, err := meta.DocumentMeta(ctx, ids)
		if err != nil {
			return nil, fmt.Errorf("selfdocs search: %w", err)
		}
		out := make([]builtin.HelpHit, len(hits))
		for i, h := range hits {
			m := metas[h.DocumentID]
			out[i] = builtin.HelpHit{
				Title: h.DocumentTitle, Excerpt: h.Content, DocsURL: h.SourceRef,
				AppPath: m["app_path"], AppLabel: m["app_label"],
			}
		}
		return out, nil
	}
}
