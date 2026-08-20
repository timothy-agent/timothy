package destinations

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/SumonMSelim/timothy/internal/brain/missions"
	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// File is one artifact file resolved and read from a mission's
// workspace, ready to attach to a delivery.
type File struct {
	Name string // base name, for display and as the sent filename
	Data []byte
}

// MaxAttachBytes bounds a single artifact file this package will read
// into memory and attach — an operator-declared plan artifact, not
// user upload, but still worth a ceiling. Telegram's own sendDocument
// cap (50MB) is higher than email's practical attachment limit, so
// this stays the smaller of the two: an oversize file is listed by
// name in the body instead of attached, for every kind.
const MaxAttachBytes = 25 << 20 // 25MB

// artifactPaths collects every workspace-relative artifact path
// declared across a mission's plan units, deduplicated, in plan order
// — exactly the paths CheckArtifacts already verified exist and are
// non-empty before the mission could reach done.
func artifactPaths(m missions.Mission) []string {
	seen := map[string]bool{}
	var out []string
	for _, u := range m.Spec.Units {
		for _, a := range u.Artifacts {
			rel := strings.TrimSpace(a)
			if rel == "" || seen[rel] {
				continue
			}
			seen[rel] = true
			out = append(out, rel)
		}
	}
	return out
}

// resolveArtifactFiles reads a mission's declared artifact files from
// its workspace for attachment. files holds every path that read
// clean and within ceiling; oversize holds the names of paths that
// exist but exceed MaxAttachBytes (listed by name in the body
// instead). A path that fails the same within-workspace guard
// CheckArtifacts uses, or that no longer reads back (moved/removed
// since verification), is silently skipped — delivery is best-effort
// and must never fail a mission over a since-vanished file.
func resolveArtifactFiles(m missions.Mission) (files []File, oversize []string) {
	root := m.WorkRoot()
	if root == "" {
		return nil, nil
	}
	for _, rel := range artifactPaths(m) {
		cleaned := filepath.Clean(rel)
		if filepath.IsAbs(rel) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
			continue
		}
		abs := filepath.Join(root, cleaned)
		if err := tools.WithinRoot(root, abs); err != nil {
			continue
		}
		info, err := os.Stat(abs)
		if err != nil || info.IsDir() {
			continue
		}
		if info.Size() > MaxAttachBytes {
			oversize = append(oversize, filepath.Base(cleaned))
			continue
		}
		data, err := os.ReadFile(abs) //nolint:gosec // G304: abs is guarded by WithinRoot above
		if err != nil {
			continue
		}
		files = append(files, File{Name: filepath.Base(cleaned), Data: data})
	}
	return files, oversize
}

// oversizeNotice renders the oversize-files line appended to a
// delivery body when any artifact exceeded MaxAttachBytes.
func oversizeNotice(oversize []string) string {
	if len(oversize) == 0 {
		return ""
	}
	return fmt.Sprintf("\n\nToo large to attach (over %dMB): %s\n", MaxAttachBytes>>20, strings.Join(oversize, ", "))
}
