import type { EChartsOption } from 'echarts'
import type { EntityEdge, EntityGraphData, EntityNode } from '../../api/types'
import { buildBaseTheme } from '../charts/theme'

// Node fill per entity kind — mid hues that read on both themes,
// consistent with the page's raw-palette convention (TypeBadge).
export const kindColor: Record<string, string> = {
  person: '#3b82f6',
  project: '#8b5cf6',
  service: '#06b6d4',
  preference: '#10b981',
  decision: '#f59e0b',
  topic: '#f43f5e',
  place: '#84cc16',
}

const unknownColor = '#6b7280'

export function colorOfKind(kind: string): string {
  return kindColor[kind] ?? unknownColor
}

const minSize = 10
const maxSize = 34

function truncate(name: string): string {
  return name.length > 18 ? `${name.slice(0, 17)}…` : name
}

// graphOptionData filters entities down to the visible kinds and drops
// any edge touching a hidden or unknown (dangling) endpoint. Exported
// for the legend chip toggle to check what would remain visible.
export function visibleEntities(data: EntityGraphData, hiddenKinds: Set<string>): EntityNode[] {
  return data.entities.filter((n) => !hiddenKinds.has(n.type))
}

function visibleEdges(edges: EntityEdge[], known: Set<string>): EntityEdge[] {
  return edges.filter((e) => known.has(e.src) && known.has(e.dst))
}

// graphOption builds the ECharts `graph` series option: force layout,
// category per entity kind, node size from memory_count (sqrt scale),
// edge width from co-occurrence weight, selected node ringed in brand.
export function graphOption(
  data: EntityGraphData,
  hiddenKinds: Set<string>,
  selectedId: string | null,
): EChartsOption {
  const theme = buildBaseTheme()
  const foreground = getComputedStyle(document.documentElement).getPropertyValue('--muted-foreground').trim()
  const border = getComputedStyle(document.documentElement).getPropertyValue('--border').trim()
  const brand = getComputedStyle(document.documentElement).getPropertyValue('--brand').trim()

  const nodes = visibleEntities(data, hiddenKinds)
  const known = new Set(nodes.map((n) => n.id))
  const edges = visibleEdges(data.edges, known)

  const maxCount = Math.max(1, ...nodes.map((n) => n.memory_count))
  const sizeOf = (count: number) => minSize + (maxSize - minSize) * Math.sqrt(count / maxCount)

  const maxWeight = Math.max(1, ...edges.map((e) => e.weight))
  const widthOf = (weight: number) => 1 + 3 * (weight / maxWeight)
  // Heavier co-occurrence pulls nodes closer: edge length is inverse
  // to weight, so the force layout compresses busiest pairs.
  const edgeLengthOf = (weight: number) => 140 - 90 * (weight / maxWeight)

  const kinds = [...new Set(data.entities.map((n) => n.type))]
  const categories = kinds.map((k) => ({ name: k, itemStyle: { color: colorOfKind(k) } }))
  const categoryIndex = new Map(kinds.map((k, i) => [k, i]))

  const seriesNodes = nodes.map((n) => ({
    id: n.id,
    name: n.name,
    value: n.memory_count,
    symbolSize: sizeOf(n.memory_count),
    category: categoryIndex.get(n.type),
    label: { formatter: () => truncate(n.name) },
    itemStyle:
      n.id === selectedId
        ? { borderColor: brand, borderWidth: 3 }
        : undefined,
  }))

  const seriesEdges = edges.map((e) => ({
    source: e.src,
    target: e.dst,
    value: e.weight,
    lineStyle: { width: widthOf(e.weight), color: border },
  }))

  // Scale force params to node count: dense graphs need more
  // repulsion and gravity to stay legible.
  const n = nodes.length
  const repulsion = 60 + 4 * n
  const gravity = n > 200 ? 0.05 : n > 50 ? 0.1 : 0.2
  const edgeLength = edges.length > 0 ? edges.map((e) => edgeLengthOf(e.weight)) : 100

  return {
    textStyle: theme.textStyle,
    tooltip: {
      trigger: 'item',
      ...theme.tooltip,
      formatter: (p) => {
        const param = p as { dataType?: string; data?: { name?: string; value?: number } }
        if (param.dataType !== 'node' || !param.data) return ''
        const n = nodes.find((x) => x.name === param.data!.name)
        if (!n) return ''
        return `${n.name}<br/>${n.type} · ${n.memory_count} ${n.memory_count === 1 ? 'memory' : 'memories'}`
      },
    },
    series: [
      {
        type: 'graph',
        layout: 'force',
        roam: true,
        draggable: true,
        scaleLimit: { min: 0.4, max: 6 },
        label: {
          show: true,
          position: 'bottom',
          color: foreground,
          fontSize: 10,
        },
        labelLayout: { hideOverlap: true },
        emphasis: { focus: 'adjacency' },
        force: { repulsion, gravity, edgeLength },
        categories,
        data: seriesNodes,
        edges: seriesEdges,
      },
    ],
  }
}
