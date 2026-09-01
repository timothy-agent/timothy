package missions

import (
	"archive/zip"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// maxFileEntries caps ListFiles' response — a mission's workspace is
// operator-facing, not a bulk export surface; WriteArchive (no cap)
// covers "I want everything."
const maxFileEntries = 2000

// FileEntry is one file the artifacts UI can list/download.
type FileEntry struct {
	Path     string    `json:"path"`
	Size     int64     `json:"size"`
	ModTime  time.Time `json:"mtime"`
	Declared bool      `json:"declared"`
}

// ListFiles walks workRoot and returns every regular file (symlinks
// excluded — they're the exfiltration vector this whole surface has to
// guard against), skipping .git entirely. declared keys are
// filepath.Clean'ed workspace-relative paths (the caller derives them
// from the mission's Plan); ListFiles itself stays decoupled from Plan
// and just looks each entry up. Stops at maxFileEntries and reports
// truncated=true rather than silently dropping the rest.
func ListFiles(workRoot string, declared map[string]bool) ([]FileEntry, bool, error) {
	var entries []FileEntry
	truncated := false
	err := filepath.WalkDir(workRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// .git is skipped whether it's a directory (a normal clone) or a
		// regular file (a linked worktree's .git is a pointer file back to
		// the parent repo's .git/worktrees/<id>) — either way it's git
		// plumbing, never a mission artifact.
		if d.Name() == ".git" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil // symlinks and other special files are never listed
		}
		rel, relErr := filepath.Rel(workRoot, path)
		if relErr != nil {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil // file disappeared mid-walk
		}
		entries = append(entries, FileEntry{
			Path:     rel,
			Size:     info.Size(),
			ModTime:  info.ModTime(),
			Declared: declared[filepath.Clean(rel)],
		})
		if len(entries) == maxFileEntries {
			truncated = true
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return entries, truncated, nil
}

// OpenFile opens rel (workspace-relative) for reading, rejecting
// anything that resolves outside workRoot (symlinks included —
// tools.WithinRoot resolves symlinks on both root and path) and
// anything that isn't a regular file.
func OpenFile(workRoot, rel string) (*os.File, fs.FileInfo, error) {
	abs := filepath.Join(workRoot, rel)
	if err := tools.WithinRoot(workRoot, abs); err != nil {
		return nil, nil, err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return nil, nil, err
	}
	if fi.IsDir() || !fi.Mode().IsRegular() {
		return nil, nil, &tools.Violation{Msg: "path is not a regular file"}
	}
	f, err := os.Open(abs) //nolint:gosec // abs is verified within workRoot by tools.WithinRoot above
	if err != nil {
		return nil, nil, err
	}
	return f, fi, nil
}

// WriteArchive streams every regular file under workRoot (same .git
// and symlink skip rules as ListFiles, uncapped) into w as a zip.
func WriteArchive(workRoot string, w io.Writer) error {
	zw := zip.NewWriter(w)
	walkErr := filepath.WalkDir(workRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() == ".git" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, relErr := filepath.Rel(workRoot, path)
		if relErr != nil {
			return nil
		}
		entryWriter, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		f, err := os.Open(path) //nolint:gosec // path comes from WalkDir rooted at workRoot; regular-file check above excludes symlinks
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(entryWriter, f)
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if walkErr != nil {
		_ = zw.Close()
		return walkErr
	}
	return zw.Close()
}

// markdownPattern matches a markdown file by extension, case-insensitive.
var markdownPattern = regexp.MustCompile(`(?i)\.(md|markdown)$`)

// readmePattern matches a top-level README, case-insensitive.
var readmePattern = regexp.MustCompile(`(?i)^readme\.(md|markdown)$`)

// OrderedMarkdownPaths picks the markdown files out of entries (flat
// ListFiles output) and orders them for a merged PDF export: folders
// before files, case-insensitive lexicographic within a directory,
// matching the web file tree's sort (fileTree.ts). A top-level
// README.md/README.markdown is hoisted to the front — a README in a
// subdirectory keeps its tree position.
func OrderedMarkdownPaths(entries []FileEntry) []string {
	var paths []string
	for _, e := range entries {
		if markdownPattern.MatchString(e.Path) {
			paths = append(paths, e.Path)
		}
	}
	sort.Slice(paths, func(i, j int) bool {
		return lessTreeOrder(paths[i], paths[j])
	})
	for i, p := range paths {
		if !strings.Contains(p, "/") && readmePattern.MatchString(p) {
			paths = append(paths[:i], paths[i+1:]...)
			paths = append([]string{p}, paths...)
			break
		}
	}
	return paths
}

// lessTreeOrder compares two workspace-relative paths the way the
// file tree sorts siblings: walk shared path segments; at the first
// segment where the two diverge, whichever path continues into a
// subdirectory there (more segments remain) sorts before the one that
// ends as a file, otherwise the diverging segment names compare
// case-insensitively.
func lessTreeOrder(a, b string) bool {
	as, bs := strings.Split(a, "/"), strings.Split(b, "/")
	for i := 0; i < len(as) && i < len(bs); i++ {
		if as[i] == bs[i] {
			continue
		}
		aIsDir, bIsDir := i < len(as)-1, i < len(bs)-1
		if aIsDir != bIsDir {
			return aIsDir // folders before files
		}
		return strings.ToLower(as[i]) < strings.ToLower(bs[i])
	}
	return len(as) < len(bs)
}
