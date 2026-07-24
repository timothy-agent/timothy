package store

import (
	"context"
	"fmt"
)

// ListEntities returns every entity with its active-memory count,
// busiest first. Zero-count entities are included — the graph shows
// them at minimum size rather than hiding them.
func (s *Store) ListEntities(ctx context.Context) ([]Entity, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("list entities: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT e.id, e.type, e.name, count(m.id)::int
		FROM entities e
		LEFT JOIN memories m ON e.id = ANY(m.entity_refs) AND m.status = $1
		GROUP BY e.id
		ORDER BY count(m.id) DESC, e.name`, StatusActive)
	if err != nil {
		return nil, fmt.Errorf("list entities: %w", err)
	}
	defer rows.Close()
	var out []Entity
	for rows.Next() {
		var e Entity
		if err := rows.Scan(&e.ID, &e.Type, &e.Name, &e.MemoryCount); err != nil {
			return nil, fmt.Errorf("list entities: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// EntityEdges derives the co-occurrence graph from entity_refs: one
// edge per entity pair that shares at least one active memory,
// weighted by how many. entity_refs has no FK, so refs whose entity
// was deleted are filtered out here rather than surfacing as ghost
// nodes. Volumes are tiny (an entity per named thing, a handful of
// refs per memory) — the pairwise unnest is fine without indexes.
func (s *Store) EntityEdges(ctx context.Context) ([]EntityEdge, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("entity edges: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT a.eid, b.eid, count(*)::int AS weight
		FROM memories m
		CROSS JOIN LATERAL unnest(m.entity_refs) AS a(eid)
		CROSS JOIN LATERAL unnest(m.entity_refs) AS b(eid)
		WHERE m.status = $1 AND a.eid < b.eid
		  AND EXISTS (SELECT 1 FROM entities WHERE id = a.eid)
		  AND EXISTS (SELECT 1 FROM entities WHERE id = b.eid)
		GROUP BY a.eid, b.eid
		ORDER BY weight DESC, a.eid, b.eid`, StatusActive)
	if err != nil {
		return nil, fmt.Errorf("entity edges: %w", err)
	}
	defer rows.Close()
	var out []EntityEdge
	for rows.Next() {
		var e EntityEdge
		if err := rows.Scan(&e.Src, &e.Dst, &e.Weight); err != nil {
			return nil, fmt.Errorf("entity edges: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListByEntity returns the active memories referencing one entity,
// newest first (detail-panel order).
func (s *Store) ListByEntity(ctx context.Context, entityID string) ([]Memory, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("list by entity: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT `+memoryColumns+` FROM memories
		WHERE $1 = ANY(entity_refs) AND status = $2
		ORDER BY created_at DESC`, entityID, StatusActive)
	if err != nil {
		return nil, fmt.Errorf("list by entity: %w", err)
	}
	defer rows.Close()
	var out []Memory
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, fmt.Errorf("list by entity: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
