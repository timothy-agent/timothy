package selfdocs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/kb"
	"github.com/SumonMSelim/timothy/internal/brain/manifest"
)

const goodManifest = `{"version":"v1","source":"manifest","files":["a.md","b.md"],"sha256":"abc"}`

const pageA = "---\ntitle: \"Tools\"\ndescription: \"Every tool.\"\napp_path: \"/settings/tools\"\napp_label: \"Tools\"\nsource: \"manifest\"\n---\n\nTool body.\n"

const pageB = "---\n# a comment\ntitle: 'Bob''s page'\ndocs_url: https://example.com/docs/b\nsource: docs\n---\nB body.\n"

func writeBundle(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		files   map[string]string
		wantErr error
		check   func(t *testing.T, b Bundle)
	}{
		{
			name:  "good bundle",
			files: map[string]string{"manifest.json": goodManifest, "a.md": pageA, "b.md": pageB, "extra.md": "not listed"},
			check: func(t *testing.T, b Bundle) {
				if b.Version != "v1" || b.SHA256 != "abc" || len(b.Pages) != 2 {
					t.Fatalf("bundle = %+v", b)
				}
				a, bb := b.Pages[0], b.Pages[1]
				if a.File != "a.md" || a.Title != "Tools" || a.Description != "Every tool." || a.AppPath != "/settings/tools" ||
					a.AppLabel != "Tools" || a.Source != "manifest" || a.Body != "Tool body." {
					t.Fatalf("page a = %+v", a)
				}
				if bb.Title != "Bob's page" || bb.DocsURL != "https://example.com/docs/b" || bb.Source != "docs" || bb.Body != "B body." {
					t.Fatalf("page b = %+v", bb)
				}
			},
		},
		{
			name: "merged release manifest",
			files: map[string]string{
				"manifest.json": `{"version":"v2","sources":["manifest","docs"],"files":["a.md","b.md"],"sha256":"def"}`,
				"a.md":          pageA, "b.md": pageB,
			},
			check: func(t *testing.T, b Bundle) {
				if b.Version != "v2" || b.SHA256 != "def" || len(b.Pages) != 2 || b.Pages[1].Source != "docs" {
					t.Fatalf("bundle = %+v", b)
				}
			},
		},
		{
			name:    "manifest.json listed as a page",
			files:   map[string]string{"manifest.json": `{"files":["manifest.json"],"sha256":"x"}`},
			wantErr: ErrInvalid,
		},
		{
			name:    "missing title",
			files:   map[string]string{"manifest.json": goodManifest, "a.md": pageA, "b.md": "---\ndescription: x\n---\nbody\n"},
			wantErr: ErrInvalid,
		},
		{
			name:    "bad manifest.json",
			files:   map[string]string{"manifest.json": `{"files":`, "a.md": pageA},
			wantErr: ErrInvalid,
		},
		{
			name:    "manifest without sha256",
			files:   map[string]string{"manifest.json": `{"files":["a.md"]}`, "a.md": pageA},
			wantErr: ErrInvalid,
		},
		{
			name:    "file listed but missing",
			files:   map[string]string{"manifest.json": goodManifest, "a.md": pageA},
			wantErr: ErrInvalid,
		},
		{
			name:    "file escaping the bundle",
			files:   map[string]string{"manifest.json": `{"files":["../a.md"],"sha256":"x"}`},
			wantErr: ErrInvalid,
		},
		{
			name:    "page without frontmatter",
			files:   map[string]string{"manifest.json": `{"files":["a.md"],"sha256":"x"}`, "a.md": "# just markdown\n"},
			wantErr: ErrInvalid,
		},
		{
			name:    "no manifest.json",
			files:   map[string]string{"a.md": pageA},
			wantErr: ErrNoBundle,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := Load(writeBundle(t, tt.files))
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			tt.check(t, b)
		})
	}
}

func TestLoadMissingDirIsNoBundle(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "absent")); !errors.Is(err, ErrNoBundle) {
		t.Fatalf("err = %v, want ErrNoBundle", err)
	}
}

// TestLoadReadsManifestWriterOutput: the generator's own output loads.
func TestLoadReadsManifestWriterOutput(t *testing.T) {
	dir := t.TempDir()
	pages := []manifest.Page{{File: "tools.md", Title: "Tools", Description: "d", AppPath: "/x", AppLabel: "X", Body: "body"}}
	if err := manifest.Write(dir, pages, "v9"); err != nil {
		t.Fatal(err)
	}
	b, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if b.Version != "v9" || len(b.Pages) != 1 || b.Pages[0].AppPath != "/x" || b.Pages[0].Source != manifest.Source {
		t.Fatalf("bundle = %+v", b)
	}
}

type fakeStore struct {
	hash     string
	replaced []kb.SystemDocument
	setHash  string
}

func (f *fakeStore) SystemBundleHash(context.Context, string) (string, error) { return f.hash, nil }

func (f *fakeStore) ReplaceSystemDocuments(_ context.Context, _, _ string, docs []kb.SystemDocument) ([]string, error) {
	f.replaced = docs
	ids := make([]string, len(docs))
	for i := range docs {
		ids[i] = docs[i].Title
	}
	return ids, nil
}

func (f *fakeStore) SetSystemBundle(_ context.Context, _, _, hash string) error {
	f.setHash = hash
	return nil
}

type fakeIngest struct {
	failOn string
	calls  []string
}

func (f *fakeIngest) IngestDocument(_ context.Context, id, _, _ string) (int, error) {
	f.calls = append(f.calls, id)
	if id == f.failOn {
		return 0, errors.New("memoryd down")
	}
	return 2, nil
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestSyncHashGate(t *testing.T) {
	tests := []struct {
		name         string
		stored       string
		failOn       string
		wantErr      bool
		wantReplaced bool
		wantIngests  int
		wantSetHash  string
	}{
		{name: "missing row", stored: "", wantReplaced: true, wantIngests: 2, wantSetHash: "abc"},
		{name: "same hash", stored: "abc"},
		{name: "different hash", stored: "old", wantReplaced: true, wantIngests: 2, wantSetHash: "abc"},
		{name: "failed ingest leaves row untouched", stored: "old", failOn: "Tools", wantErr: true, wantReplaced: true, wantIngests: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := writeBundle(t, map[string]string{"manifest.json": goodManifest, "a.md": pageA, "b.md": pageB})
			store := &fakeStore{hash: tt.stored}
			ing := &fakeIngest{failOn: tt.failOn}
			err := Sync(t.Context(), Deps{Dir: dir, Name: Collection, Store: store, Ingest: ing, Log: discard()})
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if (store.replaced != nil) != tt.wantReplaced {
				t.Fatalf("replaced = %v, want %v", store.replaced != nil, tt.wantReplaced)
			}
			if len(ing.calls) != tt.wantIngests {
				t.Fatalf("ingests = %d, want %d", len(ing.calls), tt.wantIngests)
			}
			if store.setHash != tt.wantSetHash {
				t.Fatalf("set hash = %q, want %q", store.setHash, tt.wantSetHash)
			}
		})
	}
}

func TestSyncStoresSourceAndMeta(t *testing.T) {
	dir := writeBundle(t, map[string]string{"manifest.json": goodManifest, "a.md": pageA, "b.md": pageB})
	store := &fakeStore{}
	if err := Sync(t.Context(), Deps{Dir: dir, Name: Collection, Store: store, Ingest: &fakeIngest{}, Log: discard()}); err != nil {
		t.Fatal(err)
	}
	a, b := store.replaced[0], store.replaced[1]
	if a.Meta["app_path"] != "/settings/tools" || a.Meta["app_label"] != "Tools" || a.Meta["source"] != "manifest" || a.SourceRef != "" {
		t.Fatalf("doc a = %+v", a)
	}
	if b.SourceRef != "https://example.com/docs/b" || b.Meta["source"] != "docs" || b.Markdown != "B body." {
		t.Fatalf("doc b = %+v", b)
	}
}

// TestSyncRejectsBundleWithoutTitle: one bad page stops the whole
// bundle before the store is touched.
func TestSyncRejectsBundleWithoutTitle(t *testing.T) {
	dir := writeBundle(t, map[string]string{"manifest.json": goodManifest, "a.md": pageA, "b.md": "---\nsource: docs\n---\nbody\n"})
	store := &fakeStore{hash: "old"}
	ing := &fakeIngest{}
	err := Sync(t.Context(), Deps{Dir: dir, Name: Collection, Store: store, Ingest: ing, Log: discard()})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	if store.replaced != nil || len(ing.calls) != 0 || store.setHash != "" {
		t.Fatalf("store touched: replaced=%v ingests=%d set=%q", store.replaced, len(ing.calls), store.setHash)
	}
}

// flakyIngest fails its first fails calls, as memoryd does while the
// gateway's routing config is still loading.
type flakyIngest struct {
	fails int
	calls int
}

func (f *flakyIngest) IngestDocument(context.Context, string, string, string) (int, error) {
	f.calls++
	if f.calls <= f.fails {
		return 0, errors.New("gateway http 503: config_unavailable")
	}
	return 1, nil
}

func TestSyncRetry(t *testing.T) {
	t.Run("retries until success", func(t *testing.T) {
		dir := writeBundle(t, map[string]string{"manifest.json": goodManifest, "a.md": pageA, "b.md": pageB})
		store := &fakeStore{}
		ing := &flakyIngest{fails: 2}
		var errs int
		err := SyncRetry(t.Context(), Deps{Dir: dir, Name: Collection, Store: store, Ingest: ing, Log: discard()},
			time.Millisecond, 2*time.Millisecond, func(error) { errs++ })
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if errs != 2 || ing.calls != 4 || store.setHash != "abc" {
			t.Fatalf("errs = %d, ingests = %d, set hash = %q; want 2, 4, abc", errs, ing.calls, store.setHash)
		}
	})
	t.Run("missing bundle makes one attempt", func(t *testing.T) {
		var errs int
		err := SyncRetry(t.Context(), Deps{Dir: t.TempDir(), Name: Collection, Store: &fakeStore{}, Ingest: &flakyIngest{}, Log: discard()},
			time.Millisecond, time.Millisecond, func(error) { errs++ })
		if !errors.Is(err, ErrNoBundle) || errs != 0 {
			t.Fatalf("err = %v, errs = %d; want ErrNoBundle, 0", err, errs)
		}
	})
	t.Run("cancelled context stops waiting", func(t *testing.T) {
		dir := writeBundle(t, map[string]string{"manifest.json": goodManifest, "a.md": pageA, "b.md": pageB})
		ctx, cancel := context.WithCancel(t.Context())
		store := &fakeStore{}
		err := SyncRetry(ctx, Deps{Dir: dir, Name: Collection, Store: store, Ingest: &flakyIngest{fails: 1 << 30}, Log: discard()},
			time.Hour, time.Hour, func(error) { cancel() })
		if !errors.Is(err, context.Canceled) || store.setHash != "" {
			t.Fatalf("err = %v, set hash = %q; want context.Canceled, empty", err, store.setHash)
		}
	})
}
