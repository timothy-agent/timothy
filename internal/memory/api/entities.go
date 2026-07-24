package api

import (
	"encoding/json"
	"net/http"
	"regexp"
)

// uuidPattern gates path ids before they reach a ::uuid comparison —
// an invalid literal is a client error, not a query failure.
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type entityJSON struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Name        string `json:"name"`
	MemoryCount int    `json:"memory_count"`
}

type edgeJSON struct {
	Src    string `json:"src"`
	Dst    string `json:"dst"`
	Weight int    `json:"weight"`
}

// handleEntityGraph returns the whole entity graph in one payload:
// nodes with active-memory counts plus co-occurrence edges. One
// atomic view — nodes and edges can never skew across requests.
func (a *API) handleEntityGraph(w http.ResponseWriter, r *http.Request) {
	entities, err := a.store.ListEntities(r.Context())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "graph_failed", err.Error())
		return
	}
	edges, err := a.store.EntityEdges(r.Context())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "graph_failed", err.Error())
		return
	}
	outEntities := make([]entityJSON, len(entities))
	for i, e := range entities {
		outEntities[i] = entityJSON{ID: e.ID, Type: e.Type, Name: e.Name, MemoryCount: e.MemoryCount}
	}
	outEdges := make([]edgeJSON, len(edges))
	for i, e := range edges {
		outEdges[i] = edgeJSON{Src: e.Src, Dst: e.Dst, Weight: e.Weight}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"entities": outEntities, "edges": outEdges})
}

// handleEntityMemories returns the active memories referencing one
// entity, newest first — the graph's detail panel.
func (a *API) handleEntityMemories(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !uuidPattern.MatchString(id) {
		jsonError(w, http.StatusBadRequest, "bad_request", "id must be a uuid")
		return
	}
	memories, err := a.store.ListByEntity(r.Context(), id)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "entity_memories_failed", err.Error())
		return
	}
	out := make([]memoryJSON, len(memories))
	for i, m := range memories {
		out[i] = toJSON(m)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"memories": out})
}
