package chat

import (
	"context"
	"fmt"
	"strings"

	"github.com/SumonMSelim/timothy/internal/brain/kb"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
	"github.com/SumonMSelim/timothy/internal/brain/session"
)

// Reference kinds a composer # mention can name.
const (
	ReferenceKindMission = "mission"
	ReferenceKindSession = "session"
	ReferenceKindKBDoc   = "kb_doc"
)

// Reference names one composer #-mention pick (mission/session/kb doc)
// to resolve into this turn's documents, generalizing the existing
// Knowledge (kb collection) mention onto individual missions, chats,
// and kb documents.
type Reference struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// maxReferences caps how many #-mention references one request may
// carry, same style ceiling as attachments (api/missions.go's
// maxMissionAttachments): each resolved reference's content is folded
// into every turn/prompt it reaches, so an unbounded list risks
// unbounded prompt size.
const maxReferences = 8

// referenceTranscriptCap bounds how much of a referenced session's
// transcript is folded in: a reference gives the model the session's
// content, not an invitation to re-render an unbounded chat history.
const referenceTranscriptCap = 8000

// MissionStore is the slice of *missions.Store the reference resolver
// needs. Optional (SetMissionStore); nil makes a mission reference
// resolve to nothing (skip+log), same degrade as an attachments store
// that was never wired.
type MissionStore interface {
	Get(ctx context.Context, id string) (missions.Mission, error)
	Events(ctx context.Context, id string) ([]missions.Event, error)
}

// SetMissionStore wires mission reference resolution (kind=mission).
// Optional; nil leaves every mission reference unresolved.
func (s *Service) SetMissionStore(store MissionStore) { s.missions = store }

// KBDocStore is the slice of *kb.Store the reference resolver needs
// for kind=kb_doc, separate from the existing KBRead func type because
// a reference resolves by raw document id with no collection scoping
// (a #-mention pick is an explicit, already-authorized choice, unlike
// search_kb/read_kb's agent-Knowledge-scoped reads).
type KBDocStore interface {
	GetDocument(ctx context.Context, id string) (kb.Document, error)
}

// SetKBDocStore wires kb document reference resolution (kind=kb_doc).
// Optional; nil leaves every kb_doc reference unresolved.
func (s *Service) SetKBDocStore(store KBDocStore) { s.kbDocs = store }

// ResolveReferences resolves each picked #-mention reference into a
// session.DocumentRef, the same carrier attachments already render
// through (NeutralizeSlot at packet/projection time). Over-cap is
// rejected outright (mirrors chat's own attachment cap and mission
// create's resolveAttachments); a reference naming a missing id or an
// unresolvable kind is skipped and logged, never a rejected request,
// the same soft-degrade the existing Knowledge (kb collection) mention
// already gets when a name doesn't match anything live.
func (s *Service) ResolveReferences(ctx context.Context, refs []Reference) ([]session.DocumentRef, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	if len(refs) > maxReferences {
		return nil, fmt.Errorf("chat: %w: too many references (max %d)", ErrBadRequest, maxReferences)
	}
	var docs []session.DocumentRef
	for _, ref := range refs {
		doc, ok := s.resolveOneReference(ctx, ref)
		if !ok {
			continue
		}
		docs = append(docs, doc)
	}
	return docs, nil
}

// resolveOneReference resolves a single reference, ok=false (logged)
// on a missing id, unknown kind, or a store that was never wired.
func (s *Service) resolveOneReference(ctx context.Context, ref Reference) (session.DocumentRef, bool) {
	switch ref.Kind {
	case ReferenceKindMission:
		return s.resolveMissionReference(ctx, ref.ID)
	case ReferenceKindSession:
		return s.resolveSessionReference(ctx, ref.ID)
	case ReferenceKindKBDoc:
		return s.resolveKBDocReference(ctx, ref.ID)
	default:
		s.logger.Warn("chat: reference with unknown kind skipped", "kind", ref.Kind, "id", ref.ID)
		return session.DocumentRef{}, false
	}
}

func (s *Service) resolveMissionReference(ctx context.Context, id string) (session.DocumentRef, bool) {
	if s.missions == nil {
		s.logger.Warn("chat: mission reference skipped: missions are not enabled", "id", id)
		return session.DocumentRef{}, false
	}
	m, err := s.missions.Get(ctx, id)
	if err != nil {
		s.logger.Warn("chat: mission reference skipped: not found", "id", id, "error", err)
		return session.DocumentRef{}, false
	}
	events, err := s.missions.Events(ctx, id)
	if err != nil {
		s.logger.Warn("chat: mission reference skipped: load events failed", "id", id, "error", err)
		return session.DocumentRef{}, false
	}
	digest := missions.OutcomeDigest(m, events, m.Phase, m.FailureReason)
	name := m.Name
	if name == "" {
		name = m.Goal
	}
	return session.DocumentRef{ID: m.ID, Mime: "text/plain", Markdown: digest, Name: name}, true
}

// resolveSessionReference renders a chat session's transcript the same
// way LLMContext already projects it for a live turn (D-006/D-007's
// existing projection, not a new summarization path): role-prefixed
// messages, capped to referenceTranscriptCap.
func (s *Service) resolveSessionReference(ctx context.Context, id string) (session.DocumentRef, bool) {
	meta, err := s.log.Get(ctx, id)
	if err != nil {
		s.logger.Warn("chat: session reference skipped: not found", "id", id, "error", err)
		return session.DocumentRef{}, false
	}
	events, err := s.log.Events(ctx, id)
	if err != nil {
		s.logger.Warn("chat: session reference skipped: load events failed", "id", id, "error", err)
		return session.DocumentRef{}, false
	}
	msgs, err := session.LLMContext(events, 0)
	if err != nil {
		s.logger.Warn("chat: session reference skipped: projection failed", "id", id, "error", err)
		return session.DocumentRef{}, false
	}
	var transcript string
	for _, m := range msgs {
		transcript += m.Role + ": " + m.Content + "\n\n"
	}
	name := meta.Title
	if name == "" {
		name = id
	}
	return session.DocumentRef{ID: id, Mime: "text/plain", Markdown: truncateTranscript(transcript), Name: name}, true
}

// truncateTranscript caps a rendered session transcript at
// referenceTranscriptCap bytes: a reference gives the model the
// session's content, not an invitation to re-render an unbounded chat
// history.
func truncateTranscript(s string) string {
	if len(s) <= referenceTranscriptCap {
		return s
	}
	return strings.ToValidUTF8(s[:referenceTranscriptCap], "") + "\n[truncated]"
}

func (s *Service) resolveKBDocReference(ctx context.Context, id string) (session.DocumentRef, bool) {
	if s.kbDocs == nil {
		s.logger.Warn("chat: kb doc reference skipped: knowledge base is not enabled", "id", id)
		return session.DocumentRef{}, false
	}
	doc, err := s.kbDocs.GetDocument(ctx, id)
	if err != nil {
		s.logger.Warn("chat: kb doc reference skipped: not found", "id", id, "error", err)
		return session.DocumentRef{}, false
	}
	return session.DocumentRef{ID: doc.ID, Mime: "text/plain", Markdown: doc.Markdown, Name: doc.Title}, true
}
