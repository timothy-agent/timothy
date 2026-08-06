import { cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdminAgent, AdminRoute } from '../../api/types'
import { AgentEdit } from './AgentEdit'

vi.mock('../../api/client', () => ({
  listAgents: vi.fn(),
  listRoutes: vi.fn(),
  patchAgent: vi.fn(),
  deleteAgent: vi.fn(),
  listTools: vi.fn(),
}))

import { listAgents, listRoutes, listTools } from '../../api/client'

afterEach(cleanup)

const coder: AdminAgent = {
  id: 'a1',
  name: 'coder',
  description: 'Coding missions and tasks: GLM primary, Nova reasoning fallback.',
  prompt_overlay: 'You are a careful senior engineer.',
  route: 'coding',
  skills: ['coding-task'],
  tools: ['shell', 'write_file'],
  memory: true,
  is_default: false,
  enabled: true,
  review_route: 'coding',
}

const codingRoute: AdminRoute = { name: 'coding', chain: [], strategy: 'ordered', enabled: true }

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(listAgents).mockResolvedValue([coder])
  vi.mocked(listRoutes).mockResolvedValue([codingRoute])
  vi.mocked(listTools).mockResolvedValue([])
})

function renderEdit(id = 'a1') {
  return render(
    <MemoryRouter initialEntries={[`/settings/agents/${id}`]}>
      <Routes>
        <Route path="/settings/agents/:id" element={<AgentEdit />} />
      </Routes>
    </MemoryRouter>,
  )
}

describe('AgentEdit', () => {
  it('prefills the form once the agent loads asynchronously after mount', async () => {
    renderEdit()

    // The agent arrives via a promise resolved after AgentEdit's first
    // render (before that, the fields hold blank defaults) — this
    // guards the useState-seeded-once bug: without the re-seed effect
    // in useAgentForm, these stay blank/default forever.
    expect(await screen.findByDisplayValue(coder.description)).toBeTruthy()
    expect(screen.getByDisplayValue(coder.prompt_overlay)).toBeTruthy()
    expect(screen.getByRole('combobox', { name: 'agent route' })).toHaveTextContent('coding')
  })

  it('does not show a skills allowlist field', async () => {
    renderEdit()

    await screen.findByDisplayValue(coder.description)
    expect(screen.queryByText('Skills allowlist')).not.toBeInTheDocument()
  })
})
