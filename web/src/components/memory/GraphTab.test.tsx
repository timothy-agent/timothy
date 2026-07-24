import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { EntityGraphData, MemoryItem } from '../../api/types'
import { GraphTab } from './GraphTab'

vi.mock('../../api/client', () => ({
  entityGraph: vi.fn(),
  entityMemories: vi.fn(),
  memoryChain: vi.fn(),
}))

import { entityGraph, entityMemories } from '../../api/client'

const graph: EntityGraphData = {
  entities: [
    { id: 'e1', type: 'project', name: 'timothy', memory_count: 2 },
    { id: 'e2', type: 'person', name: 'sumon', memory_count: 1 },
  ],
  edges: [{ src: 'e1', dst: 'e2', weight: 1 }],
}

const memory: MemoryItem = {
  id: 'm1',
  type: 'semantic',
  content: 'Timothy is a self-hosted assistant.',
  status: 'active',
  confidence: 0.9,
  actor: 'agent',
  created_at: '2026-07-11T10:00:00Z',
}

function renderTab() {
  return render(
    <MemoryRouter>
      <GraphTab />
    </MemoryRouter>,
  )
}

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(entityGraph).mockResolvedValue(graph)
  vi.mocked(entityMemories).mockResolvedValue([memory])
})

describe('GraphTab', () => {
  it('renders one node per entity with a legend', async () => {
    renderTab()
    const nodes = await screen.findAllByTestId('entity-node')
    expect(nodes).toHaveLength(2)
    expect(screen.getByText('project')).toBeInTheDocument()
    expect(screen.getByText('person')).toBeInTheDocument()
  })

  it('clicking a node opens the detail panel with its memories', async () => {
    renderTab()
    const nodes = await screen.findAllByTestId('entity-node')
    fireEvent.click(nodes[0])
    const panel = await screen.findByTestId('entity-detail')
    expect(panel).toHaveTextContent('timothy')
    expect(await screen.findByTestId('entity-memory')).toHaveTextContent(
      'Timothy is a self-hosted assistant.',
    )
    expect(entityMemories).toHaveBeenCalledWith('e1')
  })

  it('shows the empty state when no entities exist', async () => {
    vi.mocked(entityGraph).mockResolvedValue({ entities: [], edges: [] })
    renderTab()
    expect(await screen.findByText(/No entities yet/)).toBeInTheDocument()
  })

  it('survives an edge pointing at an unknown entity', async () => {
    vi.mocked(entityGraph).mockResolvedValue({
      entities: graph.entities,
      edges: [{ src: 'e1', dst: 'nope', weight: 3 }],
    })
    renderTab()
    expect(await screen.findAllByTestId('entity-node')).toHaveLength(2)
  })
})
