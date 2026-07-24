import type { EntityEdge, EntityNode } from '../../api/types'

// Fixed cluster order so the same kinds always land in the same
// region of the canvas across reloads.
export const ENTITY_TYPE_ORDER = [
  'person',
  'project',
  'service',
  'preference',
  'decision',
  'topic',
  'place',
] as const

export interface NodePos {
  x: number
  y: number
  r: number
}

// Golden angle in radians: successive nodes on a sunflower spiral
// pack near-uniformly without overlap checks.
const goldenAngle = 2.399963229728653

// layoutGraph places nodes deterministically: one cluster per entity
// type arranged on a circle around the canvas center, nodes within a
// cluster on a sunflower spiral, busiest first. Edges referencing
// unknown nodes (dangling entity_refs) are dropped. Pure function —
// identical input yields identical output, no randomness.
export function layoutGraph(
  nodes: EntityNode[],
  edges: EntityEdge[],
  width: number,
  height: number,
): { positions: Map<string, NodePos>; edges: EntityEdge[] } {
  const known = new Set(nodes.map((n) => n.id))
  const keptEdges = edges.filter((e) => known.has(e.src) && known.has(e.dst))

  const byType = new Map<string, EntityNode[]>()
  for (const n of nodes) {
    const list = byType.get(n.type) ?? []
    list.push(n)
    byType.set(n.type, list)
  }
  const knownOrder = ENTITY_TYPE_ORDER as readonly string[]
  const presentTypes = knownOrder
    .filter((t) => byType.has(t))
    .concat([...byType.keys()].filter((t) => !knownOrder.includes(t)).sort())

  const maxCount = Math.max(1, ...nodes.map((n) => n.memory_count))
  const radiusOf = (count: number) => 6 + 12 * Math.sqrt(count / maxCount)

  const cx = width / 2
  const cy = height / 2
  const clusterRing = 0.32 * Math.min(width, height)
  const spread = 2.4 * radiusOf(maxCount)

  const positions = new Map<string, NodePos>()
  presentTypes.forEach((type, ti) => {
    const members = [...(byType.get(type) ?? [])].sort(
      (a, b) => b.memory_count - a.memory_count || a.name.localeCompare(b.name),
    )
    const angle = -Math.PI / 2 + (ti * 2 * Math.PI) / presentTypes.length
    const ccx = presentTypes.length === 1 ? cx : cx + clusterRing * Math.cos(angle)
    const ccy = presentTypes.length === 1 ? cy : cy + clusterRing * Math.sin(angle)
    members.forEach((n, k) => {
      const theta = k * goldenAngle
      const dist = spread * Math.sqrt(k)
      const r = radiusOf(n.memory_count)
      positions.set(n.id, {
        x: clamp(ccx + dist * Math.cos(theta), r, width - r),
        y: clamp(ccy + dist * Math.sin(theta), r, height - r),
        r,
      })
    })
  })

  return { positions, edges: keptEdges }
}

function clamp(v: number, lo: number, hi: number): number {
  return Math.min(Math.max(v, lo), hi)
}
