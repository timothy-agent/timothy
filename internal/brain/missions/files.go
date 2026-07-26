package missions

import (
	"archive/zip"
	"io"
	"io/fs"
	"os"
	"path/filepath"
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
// from the mission's Spec); ListFiles itself stays decoupled from Spec
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
