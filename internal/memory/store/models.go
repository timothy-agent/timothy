package store

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// MemoryType tiers what a memory is: something that happened
// (episodic), a durable fact (semantic), or a how-to (procedural).
type MemoryType string

const (
	TypeEpisodic   MemoryType = "episodic"
	TypeSemantic   MemoryType = "semantic"
	TypeProcedural MemoryType = "procedural"
)

// Status is a memory's lifecycle stage. Agent writes land pending;
// promotion policy or user confirmation moves them to active.
// Retrieval only ever sees active rows.
type Status string

const (
	StatusPending  Status = "pending"
	StatusActive   Status = "active"
	StatusRejected Status = "rejected"
	StatusArchived Status = "archived"
)

// ActorUser marks a memory the user stated explicitly ("Timothy,
// remember…"); those skip the pending stage.
const ActorUser = "user"

// Memory is one stored fact. Content is atomic and self-contained
// (absolute dates, no pronouns needing context).
type Memory struct {
	ID              string
	Type            MemoryType
	Content         string
	Embedding       Vector
	EntityRefs      []string
	SourceSession   string
	SourceSeq       int64
	Actor           string
	CreatedAt       time.Time
	LastConfirmedAt time.Time
	SupersededBy    string
	Status          Status
	Confidence      float32
	RetrievalHits   int
}

// Entity is a named thing memories can reference. MemoryCount is the
// number of active memories referencing it (graph listings only; zero
// for entities no active memory cites).
type Entity struct {
	ID          string
	Type        string
	Name        string
	MemoryCount int
}

// EntityEdge is one co-occurrence edge of the entity graph: Weight
// active memories reference both Src and Dst. Src < Dst (deduped).
type EntityEdge struct {
	Src    string
	Dst    string
	Weight int
}

// Vector is a pgvector embedding, encoded as the extension's text
// literal ("[0.1,0.2,…]") on the wire — no driver type registration
// needed.
type Vector []float32

// String renders the pgvector text literal. Empty vector renders
// empty (callers NULLIF it).
func (v Vector) String() string {
	if len(v) == 0 {
		return ""
	}
	var b strings.Builder
	b.Grow(len(v) * 10)
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'f', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// ParseVector decodes a pgvector text literal.
func ParseVector(s string) (Vector, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
		return nil, fmt.Errorf("vector literal must be bracketed, got %q", s)
	}
	parts := strings.Split(s[1:len(s)-1], ",")
	v := make(Vector, 0, len(parts))
	for _, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 32)
		if err != nil {
			return nil, fmt.Errorf("vector element %q: %w", p, err)
		}
		v = append(v, float32(f))
	}
	return v, nil
}
