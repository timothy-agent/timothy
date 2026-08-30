package destinations

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/SumonMSelim/timothy/internal/brain/attachments"
	"github.com/SumonMSelim/timothy/internal/brain/kb"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

// artifactOpener is the slice of *attachments.Store PromoteKB needs.
type artifactOpener interface {
	Open(ctx context.Context, id string) (io.ReadCloser, attachments.Attachment, error)
}

// kbIngester runs memoryd's ingest pipeline for one document:
// api/kb.go's kbIngester satisfies this, kept as a local interface for
// the same reason artifactOpener is.
type kbIngester interface {
	IngestDocument(ctx context.Context, documentID, title, markdown string) (int, error)
}

// kbDocStore is the slice of *kb.Store PromoteKB needs.
type kbDocStore interface {
	FindDocumentBySource(ctx context.Context, sourceType, sourceRef string) (kb.Document, error)
	CreateDocument(ctx context.Context, collectionID, title, sourceType, sourceRef, provenance, markdown string, bytes int64) (string, error)
	ReplaceDocumentContent(ctx context.Context, id, title, markdown string, bytes int64, collectionID string) error
	SetIngesting(ctx context.Context, id string) error
	SetFailed(ctx context.Context, id, errMsg string) error
	GetDocument(ctx context.Context, id string) (kb.Document, error)
}

// markdownArtifactExt names the ArtifactRef extensions PromoteKB
// promotes: the same narrow set resolveArtifactFiles renders inline
// (textArtifactExts): a promoted document is prose, not an arbitrary
// binary artifact.
var markdownArtifactExt = map[string]bool{".md": true, ".txt": true}

// promoteSourceRef is the kb_documents dedup key for one mission
// artifact: mission id plus filename, so a mission with several
// markdown artifacts promotes each as its own document, and re-running
// promotion (auto on_complete plus a manual re-promote, or promoting
// twice) replaces content in place instead of duplicating rows.
func promoteSourceRef(missionID, name string) string {
	return fmt.Sprintf("mission:%s:%s", missionID, name)
}

// PromoteMission promotes a terminal mission's markdown artifact refs
// (m.ArtifactRefs, already copied into the attachment store by
// copyArtifacts before the auto-fire hook runs) into collectionID as kb
// documents with provenance='mission': D-081, issue #370. Idempotent:
// re-promoting the same mission replaces each document's content in
// place (FindDocumentBySource + ReplaceDocumentContent) rather than
// duplicating rows, the same dedup shape api/kb.go's clip re-ingest
// uses. promoted counts documents successfully promoted; errs collects
// one error per artifact that failed (a vanished/non-markdown artifact
// is silently skipped, not an error): the caller decides whether a
// partial failure matters (the manual endpoint reports it, the
// auto-fire hook just logs it).
func PromoteMission(ctx context.Context, opener artifactOpener, store kbDocStore, ingest kbIngester, m missions.Mission, collectionID string) (promoted int, errs []error) {
	for _, ref := range m.ArtifactRefs {
		if !markdownArtifactExt[strings.ToLower(filepath.Ext(ref.Name))] {
			continue
		}
		if err := promoteOne(ctx, opener, store, ingest, m.ID, collectionID, ref); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", ref.Name, err))
			continue
		}
		promoted++
	}
	return promoted, errs
}

// PromoteKB adapts PromoteMission to missions.PromoteKB, the driver's
// fire-and-forget terminal hook signature: failures are logged, never
// returned, matching CopyArtifacts' own contract.
func PromoteKB(opener artifactOpener, store kbDocStore, ingest kbIngester, log *slog.Logger) missions.PromoteKB {
	return func(ctx context.Context, m missions.Mission, collectionID string) {
		if _, errs := PromoteMission(ctx, opener, store, ingest, m, collectionID); len(errs) > 0 {
			for _, err := range errs {
				log.Warn("mission kb promotion skipped", "mission_id", m.ID, "error", err)
			}
		}
	}
}

func promoteOne(ctx context.Context, opener artifactOpener, store kbDocStore, ingest kbIngester, missionID, collectionID string, ref missions.ArtifactRef) error {
	r, att, err := opener.Open(ctx, ref.ID)
	if err != nil {
		return fmt.Errorf("open artifact: %w", err)
	}
	defer func() { _ = r.Close() }()
	data, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("read artifact: %w", err)
	}
	markdown := strings.ToValidUTF8(strings.ReplaceAll(string(data), "\x00", ""), "�")
	title := strings.TrimSuffix(ref.Name, filepath.Ext(ref.Name))
	sourceRef := promoteSourceRef(missionID, ref.Name)

	existing, err := store.FindDocumentBySource(ctx, "mission", sourceRef)
	var docID string
	if err == nil {
		if err := store.ReplaceDocumentContent(ctx, existing.ID, title, markdown, att.SizeBytes, collectionID); err != nil {
			return fmt.Errorf("replace document: %w", err)
		}
		docID = existing.ID
	} else {
		docID, err = store.CreateDocument(ctx, collectionID, title, "mission", sourceRef, "mission", markdown, att.SizeBytes)
		if err != nil {
			return fmt.Errorf("create document: %w", err)
		}
	}

	if err := store.SetIngesting(ctx, docID); err != nil {
		return fmt.Errorf("set ingesting: %w", err)
	}
	doc, err := store.GetDocument(ctx, docID)
	if err != nil {
		return fmt.Errorf("reload document: %w", err)
	}
	if ingest == nil {
		_ = store.SetFailed(ctx, docID, "memoryd is not configured")
		return fmt.Errorf("ingest: memoryd is not configured")
	}
	if _, err := ingest.IngestDocument(ctx, docID, title, doc.Markdown); err != nil {
		_ = store.SetFailed(ctx, docID, err.Error())
		return fmt.Errorf("ingest: %w", err)
	}
	return nil
}
