import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
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
  listSkills: vi.fn(),
  listKbCollections: vi.fn(),
}))

import { listAgents, listRoutes, listTools, listSkills, listKbCollections, patchAgent } from '../../api/client'

afterEach(cleanup)

const coder: AdminAgent = {
  id: 'a1',
  name: 'coder',
  description: 'Coding missions and tasks: GLM primary, Nova reasoning fallback.',
  prompt_overlay: 'You are a careful senior engineer.',
  route: 'coding',
  skills: ['coding'],
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
  vi.mocked(listSkills).mockResolvedValue([])
  vi.mocked(listKbCollections).mockResolvedValue([])
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

  it('shows a skills allowlist field', async () => {
    renderEdit()

    await screen.findByDisplayValue(coder.description)
    expect(screen.getByText('Skills allowlist')).toBeInTheDocument()
  })

  it('shows a knowledge allowlist field', async () => {
    renderEdit()

    await screen.findByDisplayValue(coder.description)
    expect(screen.getByText('Knowledge allowlist')).toBeInTheDocument()
  })

  it('includes knowledge in the save payload, defaulting to empty when the agent omits it', async () => {
    vi.mocked(patchAgent).mockResolvedValue()
    renderEdit()

    await screen.findByDisplayValue(coder.description)
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() =>
      expect(patchAgent).toHaveBeenCalledWith(
        'a1',
        expect.objectContaining({ knowledge: [] }),
      ),
    )
  })
})
