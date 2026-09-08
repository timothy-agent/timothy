import axe from 'axe-core'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
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
vi.mock('sonner', () => ({ toast: { error: vi.fn(), success: vi.fn() } }))

import { listAgents, listRoutes, listTools, listSkills, listKbCollections, patchAgent, deleteAgent } from '../../api/client'
import { toast } from 'sonner'

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
  Element.prototype.scrollIntoView = vi.fn()
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
    // render (before that, the fields hold blank defaults); this
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

  it('disables Save until the form is dirty, sends zero PATCH before Save', async () => {
    renderEdit()

    await screen.findByDisplayValue(coder.description)
    const save = screen.getByRole('button', { name: 'Save' })
    expect((save as HTMLButtonElement).disabled).toBe(true)
    expect(screen.queryByText('Unsaved changes')).toBeNull()
    expect(patchAgent).not.toHaveBeenCalled()

    fireEvent.change(screen.getByDisplayValue(coder.description), { target: { value: 'Updated description' } })
    expect((save as HTMLButtonElement).disabled).toBe(false)
    expect(screen.getByText('Unsaved changes')).toBeTruthy()
    expect(patchAgent).not.toHaveBeenCalled()
  })

  it('sends exactly one PATCH on Save with the staged fields', async () => {
    vi.mocked(patchAgent).mockResolvedValue()
    renderEdit()

    await screen.findByDisplayValue(coder.description)
    fireEvent.change(screen.getByDisplayValue(coder.description), { target: { value: 'Updated description' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() => expect(patchAgent).toHaveBeenCalledTimes(1))
    expect(patchAgent).toHaveBeenCalledWith(
      'a1',
      expect.objectContaining({ description: 'Updated description', knowledge: [] }),
    )
  })

  it('renders name as an editable field and includes it in the PATCH payload', async () => {
    vi.mocked(patchAgent).mockResolvedValue()
    renderEdit()

    fireEvent.change(await screen.findByDisplayValue(coder.name), { target: { value: 'Coder Two' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() =>
      expect(patchAgent).toHaveBeenCalledWith('a1', expect.objectContaining({ name: 'Coder Two' })),
    )
  })

  it('stages edits to overlay, route, memory, skills, and tools, all landing in the one PATCH', async () => {
    vi.mocked(patchAgent).mockResolvedValue()
    vi.mocked(listRoutes).mockResolvedValue([
      codingRoute,
      { name: 'writing', chain: [], strategy: 'ordered', enabled: true },
    ])
    renderEdit()

    await screen.findByDisplayValue(coder.description)
    fireEvent.change(screen.getByDisplayValue(coder.prompt_overlay), { target: { value: 'Be extra careful.' } })
    fireEvent.click(screen.getByRole('combobox', { name: 'agent route' }))
    fireEvent.click(await screen.findByRole('option', { name: 'writing' }))
    fireEvent.click(screen.getByRole('switch', { name: 'agent memory' }))
    fireEvent.change(screen.getByPlaceholderText('research-brief, coding'), { target: { value: 'writing' } })
    fireEvent.change(screen.getByPlaceholderText('search_web, fetch_url, shell'), { target: { value: 'shell' } })
    fireEvent.change(screen.getByPlaceholderText('product-docs, runbooks'), { target: { value: 'kb-a' } })

    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() =>
      expect(patchAgent).toHaveBeenCalledWith(
        'a1',
        expect.objectContaining({
          prompt_overlay: 'Be extra careful.',
          route: 'writing',
          memory: false,
          skills: ['writing'],
          tools: ['shell'],
          knowledge: ['kb-a'],
        }),
      ),
    )
  })

  it('Cancel restores the loaded values and clears the unsaved note', async () => {
    renderEdit()

    await screen.findByDisplayValue(coder.description)
    const descriptionInput = screen.getByDisplayValue(coder.description)
    fireEvent.change(descriptionInput, { target: { value: 'Something else' } })
    expect(screen.getByText('Unsaved changes')).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))

    expect(screen.getByDisplayValue(coder.description)).toBeTruthy()
    expect(screen.queryByText('Unsaved changes')).toBeNull()
    expect((screen.getByRole('button', { name: 'Save' }) as HTMLButtonElement).disabled).toBe(true)
    expect(patchAgent).not.toHaveBeenCalled()
  })

  it('keeps staged values and shows a retryable alert when Save fails', async () => {
    vi.mocked(patchAgent).mockRejectedValueOnce(new Error('network down'))
    renderEdit()

    await screen.findByDisplayValue(coder.description)
    fireEvent.change(screen.getByDisplayValue(coder.description), { target: { value: 'Updated description' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('network down')
    expect(screen.getByDisplayValue('Updated description')).toBeTruthy()

    vi.mocked(patchAgent).mockResolvedValueOnce()
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(patchAgent).toHaveBeenCalledTimes(2))
  })

  it('shows the harness select defaulted to inherit from settings when the agent omits it', async () => {
    renderEdit()

    await screen.findByDisplayValue(coder.description)
    expect(screen.getByRole('combobox', { name: 'agent harness' })).toHaveTextContent(
      'Inherit from settings',
    )
  })

  it('submits an empty harness (inherit) by default after an edit', async () => {
    vi.mocked(patchAgent).mockResolvedValue()
    renderEdit()

    await screen.findByDisplayValue(coder.description)
    fireEvent.change(screen.getByDisplayValue(coder.description), { target: { value: 'Updated description' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() =>
      expect(patchAgent).toHaveBeenCalledWith('a1', expect.objectContaining({ harness: '' })),
    )
  })

  it('submits the picked harness', async () => {
    vi.mocked(patchAgent).mockResolvedValue()
    renderEdit()

    await screen.findByDisplayValue(coder.description)
    fireEvent.click(screen.getByRole('combobox', { name: 'agent harness' }))
    fireEvent.click(await screen.findByRole('option', { name: 'Claude Code' }))
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() =>
      expect(patchAgent).toHaveBeenCalledWith(
        'a1',
        expect.objectContaining({ harness: 'claude-cli' }),
      ),
    )
  })

  it('has no axe violations', async () => {
    const { container } = renderEdit()
    await screen.findByDisplayValue(coder.description)
    const results = await axe.run(container, { rules: { 'color-contrast': { enabled: false } } })
    expect(results.violations).toEqual([])
  })

  it('redirects away and renders nothing when the agent id is not found', async () => {
    render(
      <MemoryRouter initialEntries={['/settings/agents/missing']}>
        <Routes>
          <Route path="/settings/agents/:id" element={<AgentEdit />} />
        </Routes>
      </MemoryRouter>,
    )

    await waitFor(() => expect(listAgents).toHaveBeenCalled())
    expect(screen.queryByRole('heading')).toBeNull()
  })

  it('shows a toast when loading the agent fails', async () => {
    vi.mocked(listAgents).mockRejectedValue(new Error('network down'))
    renderEdit()

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Could not load agent', { description: 'network down' }),
    )
  })

  it('deletes the agent behind confirm', async () => {
    vi.mocked(deleteAgent).mockResolvedValue()
    renderEdit()

    await screen.findByDisplayValue(coder.description)
    fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    const dialog = await screen.findByRole('alertdialog')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }))

    await waitFor(() => expect(deleteAgent).toHaveBeenCalledWith('a1'))
  })

  it('keeps the dialog open and toasts on delete failure', async () => {
    vi.mocked(deleteAgent).mockRejectedValue(new Error('agent in use'))
    renderEdit()

    await screen.findByDisplayValue(coder.description)
    fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    const dialog = await screen.findByRole('alertdialog')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }))

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Could not remove agent', { description: 'agent in use' }),
    )
  })

  it('describes the default agent and hides Delete', async () => {
    vi.mocked(listAgents).mockResolvedValue([{ ...coder, is_default: true }])
    renderEdit()

    await screen.findByDisplayValue(coder.description)
    expect(screen.getByText('Default agent')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Delete' })).toBeNull()
  })

  it('navigates away if the agent vanishes from the post-save refetch', async () => {
    vi.mocked(patchAgent).mockResolvedValue()
    vi.mocked(listAgents).mockResolvedValueOnce([coder]).mockResolvedValueOnce([])
    renderEdit()

    await screen.findByDisplayValue(coder.description)
    fireEvent.change(screen.getByDisplayValue(coder.description), { target: { value: 'Updated description' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() => expect(patchAgent).toHaveBeenCalledTimes(1))
    // found = null on the refetch: AgentEdit's own state flips to null
    // and it navigates away rather than rebasing a form for an agent
    // that no longer exists.
    await waitFor(() => expect(screen.queryByDisplayValue('Updated description')).toBeNull())
  })
})
