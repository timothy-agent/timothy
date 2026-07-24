import { describe, expect, it } from 'vitest'
import type { EntityEdge, EntityNode } from '../../api/types'
import { layoutGraph } from './graphLayout'

const node = (id: string, type: string, count: number): EntityNode => ({
  id,
  type,
  name: `entity-${id}`,
  memory_count: count,
})

const nodes: EntityNode[] = [
  node('a', 'person', 5),
  node('b', 'project', 3),
  node('c', 'project', 1),
  node('d', 'topic', 0),
]
const edges: EntityEdge[] = [
  { src: 'a', dst: 'b', weight: 2 },
  { src: 'a', dst: 'ghost', weight: 1 }, // dangling ref
]

describe('layoutGraph', () => {
  it('is deterministic', () => {
    const one = layoutGraph(nodes, edges, 720, 520)
    const two = layoutGraph(nodes, edges, 720, 520)
    expect([...one.positions.entries()]).toEqual([...two.positions.entries()])
    expect(one.edges).toEqual(two.edges)
  })

  it('positions every node inside the canvas', () => {
    const { positions } = layoutGraph(nodes, edges, 720, 520)
    expect(positions.size).toBe(nodes.length)
    for (const p of positions.values()) {
      expect(p.x - p.r).toBeGreaterThanOrEqual(0)
      expect(p.x + p.r).toBeLessThanOrEqual(720)
      expect(p.y - p.r).toBeGreaterThanOrEqual(0)
      expect(p.y + p.r).toBeLessThanOrEqual(520)
    }
  })

  it('sizes radius monotonically with memory count', () => {
    const { positions } = layoutGraph(nodes, edges, 720, 520)
    const r = (id: string) => positions.get(id)!.r
    expect(r('a')).toBeGreaterThan(r('b'))
    expect(r('b')).toBeGreaterThan(r('c'))
    expect(r('c')).toBeGreaterThan(r('d'))
    expect(r('d')).toBe(6) // zero-count floor
  })

  it('drops edges with unknown endpoints', () => {
    const { edges: kept } = layoutGraph(nodes, edges, 720, 520)
    expect(kept).toEqual([{ src: 'a', dst: 'b', weight: 2 }])
  })

  it('centers a single-type graph', () => {
    const { positions } = layoutGraph([node('solo', 'person', 1)], [], 720, 520)
    const p = positions.get('solo')!
    expect(p.x).toBe(360)
    expect(p.y).toBe(260)
  })
})
