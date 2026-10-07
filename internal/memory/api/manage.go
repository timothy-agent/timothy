package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/SumonMSelim/timothy/internal/memory/extract"
	"github.com/SumonMSelim/timothy/internal/memory/store"
)

// Manager is the store slice behind the queue, browser, and entity
// graph endpoints.
type Manager interface {
	ListByStatus(ctx context.Context, status store.Status, types ...store.MemoryType) ([]store.Memory, error)
	Get(ctx context.Context, id string) (store.Memory, error)
	Contents(ctx context.Context, ids []string) (map[string]string, error)
	Insert(ctx context.Context, m store.Memory) (string, error)
	NearestActive(ctx context.Context, embedding store.Vector) (id string, similarity float64, status store.Status, ok bool, err error)
	Confirm(ctx context.Context, id string) error
	Promote(ctx context.Context, id string) error
	ConfirmSuperseding(ctx context.Context, id string) error
	CorrectSuperseding(ctx context.Context, id string, m store.Memory) (string, error)
	Reject(ctx context.Context, id string) error
	Supersede(ctx context.Context, oldID, newID string) error
	Chain(ctx context.Context, id string) ([]store.Memory, error)
	ListEntities(ctx context.Context) ([]store.Entity, error)
	EntityEdges(ctx context.Context) ([]store.EntityEdge, error)
	ListByEntity(ctx context.Context, entityID string) ([]store.Memory, error)
}

type memoryJSON struct {
	ID            string          `json:"id"`
	Type          string          `json:"type"`
	Content       string          `json:"content"`
	Status        string          `json:"status"`
	Confidence    float32         `json:"confidence"`
	Actor         string          `json:"actor"`
	SourceSession string          `json:"source_session,omitempty"`
	CreatedAt     string          `json:"created_at"`
	SupersededBy  string          `json:"superseded_by,omitempty"`
	Supersedes    *supersedesJSON `json:"supersedes,omitempty"`
}

type supersedesJSON struct {
	ID      string `json:"id"`
	Content string `json:"content"`
}

func toJSON(m store.Memory) memoryJSON {
	return memoryJSON{
		ID: m.ID, Type: string(m.Type), Content: m.Content,
		Status: string(m.Status), Confidence: m.Confidence, Actor: m.Actor,
		SourceSession: m.SourceSession, CreatedAt: m.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		SupersededBy: m.SupersededBy,
	}
}

// handleList serves the confirmation queue and the browser's status
// filter. ?status defaults to pending; ?types=a,b narrows.
func (a *API) handleList(w http.ResponseWriter, r *http.Request) {
	status := store.Status(r.URL.Query().Get("status"))
	if status == "" {
		status = store.StatusPending
	}
	var types []store.MemoryType
	if raw := r.URL.Query().Get("types"); raw != "" {
		for _, t := range strings.Split(raw, ",") {
			types = append(types, store.MemoryType(t))
		}
	}
	memories, err := a.store.ListByStatus(r.Context(), status, types...)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	var previousIDs []string
	for _, m := range memories {
		if m.Supersedes != "" {
			previousIDs = append(previousIDs, m.Supersedes)
		}
	}
	previous, err := a.store.Contents(r.Context(), previousIDs)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	out := make([]memoryJSON, len(memories))
	for i, m := range memories {
		out[i] = toJSON(m)
		if content, ok := previous[m.Supersedes]; ok {
			out[i].Supersedes = &supersedesJSON{ID: m.Supersedes, Content: content}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"memories": out})
}

// handleAdd stores a user-explicit memory. Untrusted or
// credentials-adjacent content stays pending for review; clean, trusted
// memories activate (D-011).
func (a *API) handleAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Content string `json:"content"`
		Type    string `json:"type,omitempty"`
		Trusted bool   `json:"trusted,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	req.Content = strings.TrimSpace(req.Content)
	if req.Content == "" {
		jsonError(w, http.StatusBadRequest, "bad_request", "content is required")
		return
	}
	if req.Type == "" {
		req.Type = string(store.TypeSemantic)
	}

	m := store.Memory{
		Type: store.MemoryType(req.Type), Content: req.Content,
		Actor: store.ActorUser, Confidence: 1,
		RequireReview: !req.Trusted || extract.MentionsCredential(req.Content),
	}
	// Best-effort embedding: a memory without a vector still serves the
	// text and entity legs, though similarity checks are unavailable.
	if vecs, _, err := a.embed.Embed(r.Context(), []string{req.Content}, "memory-remember"); err != nil {
		a.log.Warn("remember embedding failed; stored without vector", "error", err)
	} else if len(vecs) > 0 {
		m.Embedding = store.Vector(vecs[0])
	} else {
		a.log.Warn("remember embedding returned no vector")
	}

	if len(m.Embedding) > 0 {
		dupID, similarity, status, found, err := a.store.NearestActive(r.Context(), m.Embedding)
		if err != nil {
			jsonError(w, http.StatusInternalServerError, "dedup_failed", err.Error())
			return
		}
		if found && similarity >= extract.NearDupSimilarity {
			switch status {
			case store.StatusRejected:
				// A clean, trusted restatement overrides the earlier
				// rejection as a new active row; the rejected row stays.
				if req.Trusted && !m.RequireReview {
					break
				}
				a.log.Info("memory dropped as near-duplicate of rejected fact",
					"of", dupID, "similarity", similarity)
				writeAddResult(w, "", "dropped")
				return
			case store.StatusPending:
				if req.Trusted && !m.RequireReview {
					if err := a.activate(r.Context(), dupID); err != nil {
						jsonError(w, http.StatusInternalServerError, "promote_failed", err.Error())
						return
					}
					writeAddResult(w, dupID, string(store.StatusActive))
					return
				}
				a.log.Info("memory duplicate matched pending row; kept for review",
					"of", dupID, "similarity", similarity)
				writeAddResult(w, dupID, string(store.StatusPending))
				return
			case store.StatusActive:
				prev, err := a.store.Get(r.Context(), dupID)
				if err != nil {
					jsonError(w, http.StatusInternalServerError, "dedup_failed", err.Error())
					return
				}
				// A correction supersedes the fact instead of reinforcing
				// it, matching extraction.
				if extract.IsCorrection(req.Content, prev.Content) {
					m.Supersedes = dupID
					break
				}
				if m.RequireReview {
					break
				}
				if err := a.store.Confirm(r.Context(), dupID); err != nil {
					a.log.Warn("confirm on duplicate failed; fact still dropped", "of", dupID, "error", err)
				}
				a.log.Info("memory duplicate reinforced existing row",
					"of", dupID, "similarity", similarity)
				writeAddResult(w, dupID, string(store.StatusActive))
				return
			}
		}
	}

	// A clean correction inserts pending and then supersedes atomically,
	// so the old fact never stays active beside its replacement.
	activateCorrection := m.Supersedes != "" && !m.RequireReview
	if activateCorrection {
		m.RequireReview = true
	}
	id, err := a.store.Insert(r.Context(), m)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "insert_failed", err.Error())
		return
	}
	if activateCorrection {
		if err := a.store.ConfirmSuperseding(r.Context(), id); err != nil {
			jsonError(w, http.StatusInternalServerError, "supersede_failed", err.Error())
			return
		}
		writeAddResult(w, id, string(store.StatusActive))
		return
	}
	status := store.StatusActive
	if m.RequireReview {
		status = store.StatusPending
	}
	writeAddResult(w, id, string(status))
}

// activate confirms a pending memory: a correction supersedes the fact
// it replaces, anything else is promoted.
func (a *API) activate(ctx context.Context, id string) error {
	m, err := a.store.Get(ctx, id)
	if err != nil {
		return err
	}
	if m.Supersedes != "" {
		return a.store.ConfirmSuperseding(ctx, id)
	}
	return a.store.Promote(ctx, id)
}

func writeAddResult(w http.ResponseWriter, id, status string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"id": id, "status": status})
}

// handleResolve answers a queue card: confirm, reject, or
// edit-then-confirm (the edit supersedes the original with a
// user-explicit corrected fact — never an in-place UPDATE).
func (a *API) handleResolve(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Action  string `json:"action"`
		Content string `json:"content,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	var err error
	switch req.Action {
	case "confirm":
		if edited := strings.TrimSpace(req.Content); edited != "" {
			err = a.confirmEdited(r.Context(), id, edited)
		} else {
			err = a.activate(r.Context(), id)
		}
	case "reject":
		err = a.store.Reject(r.Context(), id)
	default:
		jsonError(w, http.StatusBadRequest, "bad_request", "action must be confirm or reject")
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		jsonError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "resolve_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// confirmEdited inserts the corrected fact as user-explicit (active)
// and supersedes the pending original.
func (a *API) confirmEdited(ctx context.Context, id, content string) error {
	chain, err := a.store.Chain(ctx, id)
	if err != nil {
		return err
	}
	orig := chain[0]
	m := store.Memory{
		Type: orig.Type, Content: content, Actor: store.ActorUser,
		Confidence: 1, EntityRefs: orig.EntityRefs,
		SourceSession: orig.SourceSession, SourceSeq: orig.SourceSeq,
	}
	if vecs, _, err := a.embed.Embed(ctx, []string{content}, "memory-remember"); err != nil {
		a.log.Warn("edit embedding failed; stored without vector", "error", err)
	} else {
		m.Embedding = store.Vector(vecs[0])
	}
	if orig.Supersedes != "" {
		_, err = a.store.CorrectSuperseding(ctx, id, m)
		return err
	}
	newID, err := a.store.Insert(ctx, m)
	if err != nil {
		return err
	}
	return a.store.Supersede(ctx, id, newID)
}

// handleChain returns a memory's supersede history, oldest first.
func (a *API) handleChain(w http.ResponseWriter, r *http.Request) {
	chain, err := a.store.Chain(r.Context(), r.PathValue("id"))
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "chain_failed", err.Error())
		return
	}
	out := make([]memoryJSON, len(chain))
	for i, m := range chain {
		out[i] = toJSON(m)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"chain": out})
}
