import axe from 'axe-core'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdminAgent } from '../../api/types'
import { AgentsList } from './AgentsList'

vi.mock('../../api/client', () => ({
  listAgents: vi.fn(),
  patchAgent: vi.fn(),
  deleteAgent: vi.fn(),
  setDefaultAgent: vi.fn(),
}))
vi.mock('sonner', () => ({ toast: { error: vi.fn(), success: vi.fn() } }))

import { listAgents, patchAgent, deleteAgent, setDefaultAgent } from '../../api/client'
import { toast } from 'sonner'

afterEach(cleanup)

const general: AdminAgent = {
  id: 'a1',
  name: 'general',
  description: 'Everyday assistant.',
  prompt_overlay: '',
  route: '',
  skills: [],
  tools: [],
  memory: true,
  is_default: true,
  enabled: true,
}

const coder: AdminAgent = {
  id: 'a2',
  name: 'coder',
  description: 'Coding missions.',
  prompt_overlay: '',
  route: 'coding',
  skills: ['coding'],
  tools: ['shell'],
  memory: false,
  is_default: false,
  enabled: true,
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(listAgents).mockResolvedValue([general, coder])
})

function renderList() {
  return render(
    <MemoryRouter initialEntries={['/settings/agents']}>
      <Routes>
        <Route path="/settings/agents" element={<AgentsList />} />
      </Routes>
    </MemoryRouter>,
  )
}

describe('AgentsList', () => {
  it('renders each agent as a title link with a footer outside it', async () => {
    renderList()

    const link = await screen.findByRole('link', { name: /^coder/ })
    const footerSwitch = screen.getByRole('switch', { name: 'coder enabled' })
    expect(link.contains(footerSwitch)).toBe(false)
  })

  it('shows the default agent with a Badge, not a bare colour dot', async () => {
    renderList()

    const link = await screen.findByRole('link', { name: /^general/ })
    expect(within(link).getByText('default', { selector: '[data-slot="badge"]' })).toBeTruthy()
  })

  it('footer switch commits immediately and does not navigate', async () => {
    vi.mocked(patchAgent).mockResolvedValue()
    renderList()

    await screen.findByRole('link', { name: /^coder/ })
    fireEvent.click(screen.getByRole('switch', { name: 'coder enabled' }))

    await waitFor(() => expect(patchAgent).toHaveBeenCalledWith('a2', { enabled: false }))
    expect(await screen.findByRole('link', { name: /^coder/ })).toBeTruthy()
  })

  it('Make default is an immediate action', async () => {
    vi.mocked(setDefaultAgent).mockResolvedValue()
    renderList()

    await screen.findByRole('link', { name: /^coder/ })
    fireEvent.click(screen.getByRole('button', { name: 'Make default' }))

    await waitFor(() => expect(setDefaultAgent).toHaveBeenCalledWith('a2'))
  })

  it('deletes a non-default agent behind ConfirmDialog', async () => {
    vi.mocked(deleteAgent).mockResolvedValue()
    renderList()

    await screen.findByRole('link', { name: /^coder/ })
    fireEvent.click(screen.getByRole('button', { name: 'Delete coder' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Delete' }))

    await waitFor(() => expect(deleteAgent).toHaveBeenCalledWith('a2'))
  })

  it('has no axe violations', async () => {
    const { container } = renderList()
    await screen.findByRole('link', { name: /^coder/ })
    const results = await axe.run(container, { rules: { 'color-contrast': { enabled: false } } })
    expect(results.violations).toEqual([])
  })

  it('New agent navigates to the add page', async () => {
    render(
      <MemoryRouter initialEntries={['/settings/agents']}>
        <Routes>
          <Route path="/settings/agents" element={<AgentsList />} />
          <Route path="/settings/agents/new" element={<div>add page</div>} />
        </Routes>
      </MemoryRouter>,
    )

    await screen.findByRole('link', { name: /^coder/ })
    fireEvent.click(screen.getByRole('button', { name: 'New agent' }))
    expect(await screen.findByText('add page')).toBeTruthy()
  })

  it('Manage navigates to the agent edit page', async () => {
    render(
      <MemoryRouter initialEntries={['/settings/agents']}>
        <Routes>
          <Route path="/settings/agents" element={<AgentsList />} />
          <Route path="/settings/agents/:id" element={<div>edit page</div>} />
        </Routes>
      </MemoryRouter>,
    )

    const coderLink = await screen.findByRole('link', { name: /^coder/ })
    const card = coderLink.closest('[data-slot="card"]') as HTMLElement
    fireEvent.click(within(card).getByRole('button', { name: 'Manage' }))
    expect(await screen.findByText('edit page')).toBeTruthy()
  })

  it('cancelling the delete confirm dialog never calls deleteAgent', async () => {
    renderList()

    await screen.findByRole('link', { name: /^coder/ })
    fireEvent.click(screen.getByRole('button', { name: 'Delete coder' }))
    await screen.findByText('Delete coder?')
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))

    expect(deleteAgent).not.toHaveBeenCalled()
    expect(screen.queryByText('Delete coder?')).toBeNull()
  })

  it('toasts when loading agents fails', async () => {
    vi.mocked(listAgents).mockRejectedValue(new Error('network down'))
    renderList()

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Could not load agents', { description: 'network down' }),
    )
  })

  it('toasts and closes the dialog when delete fails', async () => {
    vi.mocked(deleteAgent).mockRejectedValue(new Error('agent in use'))
    renderList()

    await screen.findByRole('link', { name: /^coder/ })
    fireEvent.click(screen.getByRole('button', { name: 'Delete coder' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Delete' }))

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Could not remove agent', { description: 'agent in use' }),
    )
    expect(screen.queryByText('Delete coder?')).toBeNull()
  })

  it('toasts when the footer switch fails', async () => {
    vi.mocked(patchAgent).mockRejectedValue(new Error('locked'))
    renderList()

    await screen.findByRole('link', { name: /^coder/ })
    fireEvent.click(screen.getByRole('switch', { name: 'coder enabled' }))

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Could not update agent', { description: 'locked' }),
    )
  })

  it('toasts when Make default fails', async () => {
    vi.mocked(setDefaultAgent).mockRejectedValue(new Error('already default'))
    renderList()

    await screen.findByRole('link', { name: /^coder/ })
    fireEvent.click(screen.getByRole('button', { name: 'Make default' }))

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Could not set default agent', { description: 'already default' }),
    )
  })

  it('shows the empty state when there are no agents', async () => {
    vi.mocked(listAgents).mockResolvedValue([])
    renderList()

    expect(await screen.findByText('No agents configured yet')).toBeTruthy()
  })
})
