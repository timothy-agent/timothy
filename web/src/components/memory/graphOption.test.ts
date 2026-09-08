import { describe, expect, it } from 'vitest'
import type { EntityGraphData, EntityNode } from '../../api/types'
import { graphOption, visibleEntities } from './graphOption'

type Series = {
  categories: { name: string }[]
  data: { id: string; category: number; symbolSize: number }[]
  edges: { source: string; target: string }[]
  label?: { show?: boolean }
  labelLayout?: { hideOverlap?: boolean }
  force?: { layoutAnimation?: boolean }
}

function seriesOf(option: ReturnType<typeof graphOption>): Series {
  const series = option.series
  if (!Array.isArray(series)) throw new Error('expected series array')
  return series[0] as unknown as Series
}

const node = (id: string, type: string, count: number): EntityNode => ({
  id,
  type,
  name: `entity-${id}`,
  memory_count: count,
})

const data: EntityGraphData = {
  entities: [node('a', 'person', 5), node('b', 'project', 3), node('c', 'project', 0)],
  edges: [
    { src: 'a', dst: 'b', weight: 2 },
    { src: 'a', dst: 'ghost', weight: 1 },
  ],
}

describe('graphOption', () => {
  it('assigns one category per entity kind present', () => {
    const s = seriesOf(graphOption(data, new Set()))
    expect(s.categories.map((c) => c.name)).toEqual(['person', 'project'])
    const a = s.data.find((n) => n.id === 'a')!
    const b = s.data.find((n) => n.id === 'b')!
    expect(a.category).toBe(0)
    expect(b.category).toBe(1)
  })

  it('drops hidden-kind nodes and their edges', () => {
    const s = seriesOf(graphOption(data, new Set(['project'])))
    expect(s.data.map((n) => n.id)).toEqual(['a'])
    expect(s.edges).toHaveLength(0)
  })

  it('drops edges with a dangling endpoint', () => {
    const s = seriesOf(graphOption(data, new Set()))
    expect(s.edges).toHaveLength(1)
    expect(s.edges[0]).toMatchObject({ source: 'a', target: 'b' })
  })

  it('sizes nodes monotonically with memory_count', () => {
    const s = seriesOf(graphOption(data, new Set()))
    const size = (id: string) => s.data.find((n) => n.id === id)!.symbolSize
    expect(size('a')).toBeGreaterThan(size('b'))
    expect(size('b')).toBeGreaterThan(size('c'))
  })

  function manyNodes(count: number): EntityGraphData {
    return {
      entities: Array.from({ length: count }, (_, i) => node(`n${i}`, 'topic', 1)),
      edges: [],
    }
  }

  it('hides overlap labels below the dense threshold', () => {
    const s = seriesOf(graphOption(manyNodes(20), new Set()))
    expect(s.labelLayout?.hideOverlap).toBe(true)
    expect(s.label?.show).toBe(true)
  })

  it('shows labels on emphasis only above the dense threshold', () => {
    const s = seriesOf(graphOption(manyNodes(200), new Set()))
    expect(s.labelLayout).toBeUndefined()
    expect(s.label?.show).toBe(false)
  })

  it('disables force layout animation for very large graphs', () => {
    const small = seriesOf(graphOption(manyNodes(200), new Set()))
    const large = seriesOf(graphOption(manyNodes(400), new Set()))
    expect(small.force?.layoutAnimation).toBe(true)
    expect(large.force?.layoutAnimation).toBe(false)
  })
})

describe('visibleEntities', () => {
  it('filters out hidden kinds', () => {
    const kept = visibleEntities(data, new Set(['project']))
    expect(kept.map((n) => n.id)).toEqual(['a'])
  })
})
