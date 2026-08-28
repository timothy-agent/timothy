// Package attachments stores user-uploaded images and PDFs
// content-addressed on a local volume, with metadata only in Postgres
// — binaries never enter the database (D-045). ATTACHMENTS_DIR unset
// means the feature is off: callers nil-gate on a nil *Store.
package attachments

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

// MaxSizeBytes caps a single attachment upload; enforced by the caller
// via http.MaxBytesReader before Save ever sees the body.
const MaxSizeBytes = 10 << 20 // 10MB

// ErrUnsupportedMIME reports a sniffed content type outside the
// allowlist — decided server-side (http.DetectContentType), never
// trusting the client-supplied header.
var ErrUnsupportedMIME = errors.New("attachments: unsupported mime type")

// ErrNotFound reports an id with no matching row.
var ErrNotFound = errors.New("attachments: not found")

// allowedExt maps the sniffed MIME type (bare, parameters stripped) to
// its stored file extension. Only these six are accepted; anything
// else is ErrUnsupportedMIME.
var allowedExt = map[string]string{
	"image/png":       ".png",
	"image/jpeg":      ".jpg",
	"image/webp":      ".webp",
	"image/gif":       ".gif",
	"application/pdf": ".pdf",
	"text/plain":      ".txt",
}

// Attachment is one stored image's metadata.
type Attachment struct {
	ID        string // sha256 hex of the file bytes
	Mime      string
	SizeBytes int64
	CreatedAt time.Time
}

// Store persists attachment bytes under dir (content-addressed,
// <sha256><ext>) and metadata in the attachments table.
type Store struct {
	dir string
	db  *pgpool.Pool
}

// New returns a Store rooted at dir. Callers nil-gate the feature by
// only calling New when ATTACHMENTS_DIR is set.
func New(dir string, db *pgpool.Pool) *Store {
	return &Store{dir: dir, db: db}
}

// Save streams r to a temp file while computing its sha256, sniffs the
// MIME type from the leading bytes (never the caller's declared
// mime), and on success renames into place as <sha256><ext>. Content-
// addressed: saving identical bytes twice yields the same id and is a
// no-op on the metadata row (ON CONFLICT DO NOTHING).
func (s *Store) Save(ctx context.Context, r io.Reader) (Attachment, error) {
	tmp, err := os.CreateTemp(s.dir, "upload-*")
	if err != nil {
		return Attachment{}, fmt.Errorf("attachments: save: temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = os.Remove(tmpPath) // no-op once renamed into place
	}()

	h := sha256.New()
	// http.DetectContentType only inspects the first 512 bytes.
	var sniffBuf [512]byte
	sniffed := 0

	size, err := copyAndSniff(tmp, r, h, sniffBuf[:], &sniffed)
	if err != nil {
		_ = tmp.Close()
		return Attachment{}, fmt.Errorf("attachments: save: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return Attachment{}, fmt.Errorf("attachments: save: close: %w", err)
	}

	sniffedMime := http.DetectContentType(sniffBuf[:sniffed])
	bareMime := sniffedMime
	if parsed, _, err := mime.ParseMediaType(sniffedMime); err == nil {
		bareMime = parsed
	}
	ext, ok := allowedExt[bareMime]
	if !ok {
		return Attachment{}, fmt.Errorf("attachments: save: mime %q: %w", sniffedMime, ErrUnsupportedMIME)
	}

	id := fmt.Sprintf("%x", h.Sum(nil))
	finalPath := filepath.Join(s.dir, id+ext)
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return Attachment{}, fmt.Errorf("attachments: save: rename: %w", err)
	}

	db, err := s.db.Get()
	if err != nil {
		return Attachment{}, fmt.Errorf("attachments: save: %w", err)
	}
	// Content-addressed: identical bytes hash to the same id, so a
	// repeat upload is a no-op here — the file rename above is
	// likewise a same-content overwrite of itself.
	if _, err := db.Exec(ctx,
		"INSERT INTO attachments (id, mime, size_bytes) VALUES ($1, $2, $3) ON CONFLICT (id) DO NOTHING",
		id, bareMime, size,
	); err != nil {
		return Attachment{}, fmt.Errorf("attachments: save: insert: %w", err)
	}
	var createdAt time.Time
	if err := db.QueryRow(ctx,
		"SELECT created_at FROM attachments WHERE id = $1", id,
	).Scan(&createdAt); err != nil {
		return Attachment{}, fmt.Errorf("attachments: save: read back: %w", err)
	}

	return Attachment{ID: id, Mime: bareMime, SizeBytes: size, CreatedAt: createdAt}, nil
}

// copyAndSniff copies r into w while hashing every byte and capturing
// up to len(sniffBuf) leading bytes for MIME detection.
func copyAndSniff(w io.Writer, r io.Reader, h io.Writer, sniffBuf []byte, sniffed *int) (int64, error) {
	mw := io.MultiWriter(w, h)
	var total int64
	buf := make([]byte, 32*1024)
	for {
		n, rerr := r.Read(buf)
		if n > 0 {
			if _, err := mw.Write(buf[:n]); err != nil {
				return total, err
			}
			if *sniffed < len(sniffBuf) {
				*sniffed += copy(sniffBuf[*sniffed:], buf[:n])
			}
			total += int64(n)
		}
		if rerr == io.EOF {
			return total, nil
		}
		if rerr != nil {
			return total, rerr
		}
	}
}

// Get returns metadata for id without opening the file.
func (s *Store) Get(ctx context.Context, id string) (Attachment, error) {
	db, err := s.db.Get()
	if err != nil {
		return Attachment{}, fmt.Errorf("attachments: get: %w", err)
	}
	var a Attachment
	err = db.QueryRow(ctx,
		"SELECT id, mime, size_bytes, created_at FROM attachments WHERE id = $1", id,
	).Scan(&a.ID, &a.Mime, &a.SizeBytes, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Attachment{}, ErrNotFound
	}
	if err != nil {
		return Attachment{}, fmt.Errorf("attachments: get: %w", err)
	}
	return a, nil
}

// Open returns the attachment's bytes plus its metadata. The caller
// must Close the reader.
func (s *Store) Open(ctx context.Context, id string) (io.ReadCloser, Attachment, error) {
	a, err := s.Get(ctx, id)
	if err != nil {
		return nil, Attachment{}, err
	}
	ext := allowedExt[a.Mime]
	f, err := os.Open(filepath.Join(s.dir, a.ID+ext))
	if err != nil {
		return nil, Attachment{}, fmt.Errorf("attachments: open: %w", err)
	}
	return f, a, nil
}
