import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdminRoute } from '../../api/types'
import { AgentAdd } from './AgentAdd'

vi.mock('../../api/client', () => ({
  createAgent: vi.fn(),
  listRoutes: vi.fn(),
  listTools: vi.fn(),
  listSkills: vi.fn(),
  listKbCollections: vi.fn(),
}))
vi.mock('sonner', () => ({ toast: { error: vi.fn(), success: vi.fn() } }))

import { createAgent, listRoutes, listTools, listSkills, listKbCollections } from '../../api/client'
import { toast } from 'sonner'

afterEach(cleanup)
beforeEach(() => {
  Element.prototype.scrollIntoView = vi.fn()
  vi.clearAllMocks()
  vi.mocked(listRoutes).mockResolvedValue([])
  vi.mocked(listTools).mockResolvedValue([])
  vi.mocked(listSkills).mockResolvedValue([])
  vi.mocked(listKbCollections).mockResolvedValue([])
})

function renderAdd() {
  return render(
    <MemoryRouter initialEntries={['/settings/agents/new']}>
      <Routes>
        <Route path="/settings/agents/new" element={<AgentAdd />} />
        <Route path="/settings/agents" element={<div>agents list</div>} />
      </Routes>
    </MemoryRouter>,
  )
}

describe('AgentAdd', () => {
  it('disables Create agent until a name is entered', async () => {
    renderAdd()

    const create = await screen.findByRole('button', { name: 'Create agent' })
    expect((create as HTMLButtonElement).disabled).toBe(true)

    fireEvent.change(screen.getByPlaceholderText('infra, homelab, writer…'), { target: { value: 'infra' } })
    expect((create as HTMLButtonElement).disabled).toBe(false)
  })

  it('creates the agent with slugified name and navigates to the list', async () => {
    vi.mocked(createAgent).mockResolvedValue(undefined)
    renderAdd()

    fireEvent.change(await screen.findByPlaceholderText('infra, homelab, writer…'), {
      target: { value: 'My Infra Agent' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Create agent' }))

    await waitFor(() =>
      expect(createAgent).toHaveBeenCalledWith(
        expect.objectContaining({ name: 'my-infra-agent', enabled: true, memory: true, harness: '' }),
      ),
    )
    expect(await screen.findByText('agents list')).toBeTruthy()
  })

  it('shows a toast and stays on the page when create fails', async () => {
    vi.mocked(createAgent).mockRejectedValue(new Error('name already exists'))
    renderAdd()

    fireEvent.change(await screen.findByPlaceholderText('infra, homelab, writer…'), { target: { value: 'infra' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create agent' }))

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Could not create agent', { description: 'name already exists' }),
    )
    expect(screen.queryByText('agents list')).toBeNull()
  })

  it('Cancel navigates to the agents list with no create call', async () => {
    renderAdd()

    await screen.findByPlaceholderText('infra, homelab, writer…')
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))

    expect(await screen.findByText('agents list')).toBeTruthy()
    expect(createAgent).not.toHaveBeenCalled()
  })

  it('loads and offers routes from the picker', async () => {
    const codingRoute: AdminRoute = { name: 'coding', chain: [], strategy: 'ordered', enabled: true }
    vi.mocked(listRoutes).mockResolvedValue([codingRoute])
    renderAdd()

    fireEvent.click(await screen.findByRole('combobox', { name: 'agent route' }))
    expect(await screen.findByRole('option', { name: 'coding' })).toBeTruthy()
  })

  it('picking a route, editing the overlay, toggling memory, and setting skills all land in the create payload', async () => {
    const codingRoute: AdminRoute = { name: 'coding', chain: [], strategy: 'ordered', enabled: true }
    vi.mocked(listRoutes).mockResolvedValue([codingRoute])
    vi.mocked(createAgent).mockResolvedValue(undefined)
    renderAdd()

    fireEvent.change(await screen.findByPlaceholderText('infra, homelab, writer…'), { target: { value: 'infra' } })
    fireEvent.change(
      screen.getByPlaceholderText('Instructions, persona, house rules… Markdown supported.'),
      { target: { value: 'Be careful.' } },
    )
    fireEvent.click(screen.getByRole('combobox', { name: 'agent route' }))
    fireEvent.click(await screen.findByRole('option', { name: 'coding' }))
    fireEvent.click(screen.getByRole('switch', { name: 'agent memory' }))
    fireEvent.change(screen.getByPlaceholderText('research-brief, coding'), { target: { value: 'coding' } })

    fireEvent.click(screen.getByRole('button', { name: 'Create agent' }))

    await waitFor(() =>
      expect(createAgent).toHaveBeenCalledWith(
        expect.objectContaining({
          prompt_overlay: 'Be careful.',
          route: 'coding',
          memory: false,
          skills: ['coding'],
        }),
      ),
    )
  })

  it('picking a harness lands it in the create payload', async () => {
    vi.mocked(createAgent).mockResolvedValue(undefined)
    renderAdd()

    fireEvent.change(await screen.findByPlaceholderText('infra, homelab, writer…'), { target: { value: 'infra' } })
    fireEvent.click(screen.getByRole('combobox', { name: 'agent harness' }))
    fireEvent.click(await screen.findByRole('option', { name: 'Claude Code' }))
    fireEvent.click(screen.getByRole('button', { name: 'Create agent' }))

    await waitFor(() =>
      expect(createAgent).toHaveBeenCalledWith(expect.objectContaining({ harness: 'claude-cli' })),
    )
  })
})
