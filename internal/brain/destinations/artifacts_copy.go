package destinations

import (
	"bytes"
	"context"
	"io"
	"log/slog"

	"github.com/SumonMSelim/timothy/internal/brain/attachments"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

// artifactSaver is the slice of *attachments.Store CopyArtifacts
// needs — mirrors chat.AttachmentStore's own narrow interface, kept
// local rather than depending on the concrete *attachments.Store type.
type artifactSaver interface {
	Save(ctx context.Context, r io.Reader) (attachments.Attachment, error)
}

// CopyArtifacts best-effort copies a terminal mission's declared
// artifact files (the same set resolveArtifactFiles reads for
// delivery) into the attachment store, returning the resulting refs.
// A vanished/oversize/disallowed-mime file is silently skipped — see
// resolveArtifactFiles's own doc comment for that contract; this adds
// no further failure mode of its own besides the store's Save call,
// which is likewise skipped on error rather than aborting the rest.
// Never errors and never fails the mission's terminal transition.
func CopyArtifacts(saver artifactSaver, log *slog.Logger) missions.ArtifactCopy {
	return func(ctx context.Context, m missions.Mission) []missions.ArtifactRef {
		files, texts, _ := resolveArtifactFiles(m)
		var refs []missions.ArtifactRef
		save := func(name string, data []byte) {
			a, err := saver.Save(ctx, bytes.NewReader(data))
			if err != nil {
				log.Warn("mission artifact copy skipped", "mission_id", m.ID, "name", name, "error", err)
				return
			}
			refs = append(refs, missions.ArtifactRef{ID: a.ID, Mime: a.Mime, Name: name})
		}
		for _, f := range files {
			save(f.Name, f.Data)
		}
		for _, t := range texts {
			save(t.Name, []byte(t.Content))
		}
		return refs
	}
}
