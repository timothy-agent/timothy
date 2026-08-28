package destinations

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/attachments"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

// fakeSaver stubs artifactSaver: every save gets a sequential id, or
// errors for a name in reject.
type fakeSaver struct {
	n      int
	reject map[string]bool
}

func (f *fakeSaver) Save(_ context.Context, r io.Reader) (attachments.Attachment, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return attachments.Attachment{}, err
	}
	if f.reject[string(data)] {
		return attachments.Attachment{}, errors.New("unsupported mime")
	}
	f.n++
	return attachments.Attachment{ID: fmt.Sprintf("id-%d", f.n), Mime: "text/plain"}, nil
}

func TestCopyArtifactsHappyPath(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "out.md"), []byte("report body"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "data.csv"), []byte("a,b\n1,2\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	m := missions.Mission{ID: "m1", Workspace: root, Spec: missions.Spec{Units: []missions.PlanUnit{
		{Artifacts: []string{"out.md", "data.csv"}},
	}}}

	saver := &fakeSaver{}
	refs := CopyArtifacts(saver, slog.Default())(t.Context(), m)
	if len(refs) != 2 {
		t.Fatalf("refs = %+v, want 2", refs)
	}
	names := map[string]bool{}
	for _, r := range refs {
		names[r.Name] = true
		if r.ID == "" || r.Mime == "" {
			t.Fatalf("ref missing id/mime: %+v", r)
		}
	}
	if !names["out.md"] || !names["data.csv"] {
		t.Fatalf("refs = %+v, want out.md and data.csv", refs)
	}
}

func TestCopyArtifactsSkipsSaveFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ok.md"), []byte("fine"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "bad.html"), []byte("<html>rejected</html>"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	m := missions.Mission{ID: "m1", Workspace: root, Spec: missions.Spec{Units: []missions.PlanUnit{
		{Artifacts: []string{"ok.md", "bad.html"}},
	}}}

	saver := &fakeSaver{reject: map[string]bool{"<html>rejected</html>": true}}
	refs := CopyArtifacts(saver, slog.Default())(t.Context(), m)
	if len(refs) != 1 || refs[0].Name != "ok.md" {
		t.Fatalf("refs = %+v, want only ok.md (bad.html's save failure must not fail the copy)", refs)
	}
}

func TestCopyArtifactsNothingDeclared(t *testing.T) {
	m := missions.Mission{ID: "m1"}
	saver := &fakeSaver{}
	refs := CopyArtifacts(saver, slog.Default())(t.Context(), m)
	if len(refs) != 0 {
		t.Fatalf("refs = %+v, want none", refs)
	}
}
