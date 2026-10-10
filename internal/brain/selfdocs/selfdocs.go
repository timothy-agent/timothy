// Package selfdocs ingests the documentation bundle shipped with a
// Timothy build (cmd/manifest pages plus the docs site) into a system
// knowledge collection, once per bundle hash (issue #1126).
package selfdocs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/SumonMSelim/timothy/internal/brain/kb"
)

// Collection names the system collection and its kb_system_bundles row.
const Collection = "timothy-docs"

// Description is the system collection's description.
const Description = "Timothy's own documentation for this version"

// ErrNoBundle reports a directory without a manifest.json.
var ErrNoBundle = errors.New("selfdocs: no bundle")

// ErrInvalid wraps every rejected bundle.
var ErrInvalid = errors.New("selfdocs: invalid bundle")

// Page is one markdown file of the bundle.
type Page struct {
	File        string
	Title       string
	Description string
	DocsURL     string
	AppPath     string
	AppLabel    string
	Source      string
	Body        string
}

// Bundle is a loaded bundle: SHA256 is the manifest's content hash.
type Bundle struct {
	Version string
	SHA256  string
	Pages   []Page
}

type index struct {
	Version string   `json:"version"`
	Files   []string `json:"files"`
	SHA256  string   `json:"sha256"`
}

// Load reads dir/manifest.json and every page it lists. Files on disk
// that manifest.json does not list are ignored.
func Load(dir string) (Bundle, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json")) //nolint:gosec // G304: dir is operator config (SELFDOCS_DIR).
	if errors.Is(err, fs.ErrNotExist) {
		return Bundle{}, fmt.Errorf("%w in %s", ErrNoBundle, dir)
	}
	if err != nil {
		return Bundle{}, fmt.Errorf("selfdocs: read manifest.json: %w", err)
	}
	var m index
	if err := json.Unmarshal(raw, &m); err != nil {
		return Bundle{}, fmt.Errorf("%w: manifest.json: %w", ErrInvalid, err)
	}
	if m.SHA256 == "" {
		return Bundle{}, fmt.Errorf("%w: manifest.json has no sha256", ErrInvalid)
	}
	if len(m.Files) == 0 {
		return Bundle{}, fmt.Errorf("%w: manifest.json lists no files", ErrInvalid)
	}
	b := Bundle{Version: m.Version, SHA256: m.SHA256, Pages: make([]Page, 0, len(m.Files))}
	for _, f := range m.Files {
		if !filepath.IsLocal(f) || filepath.Ext(f) != ".md" {
			return Bundle{}, fmt.Errorf("%w: file %q", ErrInvalid, f)
		}
		content, err := os.ReadFile(filepath.Join(dir, f)) //nolint:gosec // G304: f passed filepath.IsLocal above.
		if err != nil {
			return Bundle{}, fmt.Errorf("%w: %s: %w", ErrInvalid, f, err)
		}
		p, err := parsePage(string(content))
		if err != nil {
			return Bundle{}, fmt.Errorf("%w: %s: %w", ErrInvalid, f, err)
		}
		p.File = f
		b.Pages = append(b.Pages, p)
	}
	return b, nil
}

// parsePage splits flat `key: value` frontmatter from the body. Values
// may be bare, single-quoted or JSON double-quoted.
func parsePage(content string) (Page, error) {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	rest, ok := strings.CutPrefix(content, "---\n")
	if !ok {
		return Page{}, errors.New("no frontmatter")
	}
	front, body, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		return Page{}, errors.New("unterminated frontmatter")
	}
	fields := map[string]string{}
	for line := range strings.SplitSeq(front, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return Page{}, fmt.Errorf("frontmatter line %q is not key: value", line)
		}
		v, err := scalar(strings.TrimSpace(value))
		if err != nil {
			return Page{}, fmt.Errorf("frontmatter %s: %w", strings.TrimSpace(key), err)
		}
		fields[strings.TrimSpace(key)] = v
	}
	p := Page{
		Title: fields["title"], Description: fields["description"], DocsURL: fields["docs_url"],
		AppPath: fields["app_path"], AppLabel: fields["app_label"], Source: fields["source"],
		Body: strings.TrimSpace(body),
	}
	if p.Title == "" {
		return Page{}, errors.New("frontmatter has no title")
	}
	if p.Body == "" {
		return Page{}, errors.New("page has no body")
	}
	return p, nil
}

func scalar(v string) (string, error) {
	switch {
	case len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"':
		var s string
		if err := json.Unmarshal([]byte(v), &s); err != nil {
			return "", fmt.Errorf("bad quoted value %s: %w", v, err)
		}
		return s, nil
	case len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'':
		return strings.ReplaceAll(v[1:len(v)-1], "''", "'"), nil
	default:
		return v, nil
	}
}

// Store is the slice of *kb.Store Sync writes through; tests fake it.
type Store interface {
	SystemBundleHash(ctx context.Context, name string) (string, error)
	ReplaceSystemDocuments(ctx context.Context, name, description string, docs []kb.SystemDocument) ([]string, error)
	SetSystemBundle(ctx context.Context, name, version, hash string) error
}

// Ingester is memoryd's ingest call; *memclient.Client satisfies it.
type Ingester interface {
	IngestDocument(ctx context.Context, documentID, title, markdown string) (int, error)
}

// Deps is what Sync needs. Name is the collection, Collection in
// production.
type Deps struct {
	Dir    string
	Name   string
	Store  Store
	Ingest Ingester
	Log    *slog.Logger
}

// Sync loads the bundle in d.Dir and, when its hash differs from the
// one last ingested in full, replaces the collection's documents and
// ingests each through memoryd. The bundle row moves only after every
// page ingested, so a failure retries on the next boot. A missing
// bundle is ErrNoBundle.
func Sync(ctx context.Context, d Deps) error {
	b, err := Load(d.Dir)
	if err != nil {
		return err
	}
	stored, err := d.Store.SystemBundleHash(ctx, d.Name)
	if err != nil {
		return fmt.Errorf("selfdocs: %w", err)
	}
	if stored == b.SHA256 {
		d.Log.Info("selfdocs: bundle unchanged, skipping ingest", "collection", d.Name, "version", b.Version)
		return nil
	}
	docs := make([]kb.SystemDocument, len(b.Pages))
	for i, p := range b.Pages {
		docs[i] = kb.SystemDocument{
			Title: p.Title, SourceRef: p.DocsURL, Markdown: p.Body,
			Meta: map[string]string{"app_path": p.AppPath, "app_label": p.AppLabel, "source": p.Source},
		}
	}
	ids, err := d.Store.ReplaceSystemDocuments(ctx, d.Name, Description, docs)
	if err != nil {
		return fmt.Errorf("selfdocs: %w", err)
	}
	chunks := 0
	for i, id := range ids {
		n, err := d.Ingest.IngestDocument(ctx, id, docs[i].Title, docs[i].Markdown)
		if err != nil {
			return fmt.Errorf("selfdocs: ingest %s: %w", b.Pages[i].File, err)
		}
		chunks += n
	}
	if err := d.Store.SetSystemBundle(ctx, d.Name, b.Version, b.SHA256); err != nil {
		return fmt.Errorf("selfdocs: %w", err)
	}
	d.Log.Info("selfdocs: bundle ingested", "collection", d.Name, "version", b.Version, "pages", len(ids), "chunks", chunks)
	return nil
}
