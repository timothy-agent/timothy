import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ChatIntent } from './Home'
import { Home } from './Home'

vi.mock('../api/client', () => ({
  listAgents: vi.fn(),
  listMemories: vi.fn(),
  listRoutes: vi.fn(),
  getSettings: vi.fn().mockResolvedValue({ settings: { transcribe_enabled: false }, values: {} }),
  listKbCollections: vi.fn().mockResolvedValue([]),
}))

import type { AdminRoute } from '../api/types'
import { listAgents, listKbCollections, listMemories, listRoutes } from '../api/client'

let landed: { pathname: string; state: ChatIntent | null } | null = null

function ChatProbe() {
  const location = useLocation()
  landed = { pathname: location.pathname, state: location.state as ChatIntent | null }
  return <div>chat page</div>
}

function renderHome() {
  return render(
    <MemoryRouter initialEntries={['/']}>
      <Routes>
        <Route path="/" element={<Home />} />
        <Route path="/chat" element={<ChatProbe />} />
        <Route path="/memory" element={<div>memory page</div>} />
        <Route path="/research" element={<div>research page</div>} />
        <Route path="/analytics" element={<div>analytics page</div>} />
      </Routes>
    </MemoryRouter>,
  )
}

const generalAgent = {
  id: 'a1',
  name: 'general',
  description: 'Everyday chat',
  prompt_overlay: '',
  route: '',
  skills: [],
  tools: [],
  memory: true,
  is_default: true,
  enabled: true,
}
const researchAgent = {
  id: 'a2',
  name: 'research',
  description: 'Deep research with citations',
  prompt_overlay: '',
  route: '',
  skills: ['research-brief'],
  tools: [],
  memory: true,
  is_default: false,
  enabled: true,
}
const routes: AdminRoute[] = [{ name: 'summarize', strategy: 'ordered', enabled: true, chain: [] }]

// Radix opens dropdowns on pointerdown, not click.
function openMenu(name: string) {
  const trigger = screen.getByRole('button', { name })
  fireEvent.pointerDown(trigger, { button: 0, pointerId: 1 })
  fireEvent.click(trigger)
}

afterEach(cleanup)
beforeEach(() => {
  landed = null
  vi.clearAllMocks()
  vi.mocked(listAgents).mockResolvedValue([generalAgent, researchAgent])
  vi.mocked(listMemories).mockResolvedValue([])
  vi.mocked(listRoutes).mockRejectedValue(new Error('not used on this page'))
  localStorage.clear()
})

describe('Home', () => {
  it('shows the agent cards', async () => {
    renderHome()
    expect(await screen.findByRole('heading', { name: 'Agents' })).toBeTruthy()
    expect(await screen.findByRole('button', { name: /general/ })).toBeTruthy()
    expect(screen.getByRole('button', { name: /research/ })).toBeTruthy()
    expect(screen.getByText('Default')).toBeTruthy()
  })

  it('submitting the composer starts a chat with the message', () => {
    renderHome()
    const input = screen.getByLabelText('Message')
    fireEvent.change(input, { target: { value: 'hello there' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(landed?.pathname).toBe('/chat')
    expect(landed?.state?.send).toBe('hello there')
    // Agent may be '' (server default) — the intent just carries it.
    expect(landed?.state).toHaveProperty('agent')
  })

  it('submitting with a picked route includes it in the navigate state', async () => {
    vi.mocked(listRoutes).mockResolvedValue(routes)
    renderHome()

    await screen.findByRole('button', { name: 'Agent and route' })
    openMenu('Agent and route')
    fireEvent.click(await screen.findByText('summarize'))

    const input = screen.getByLabelText('Message')
    fireEvent.change(input, { target: { value: 'hello there' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(landed?.state?.route).toBe('summarize')
    expect(localStorage.getItem('timothy.route')).toBe('summarize')
  })

  it('an empty composer does not navigate', () => {
    renderHome()
    const input = screen.getByLabelText('Message')
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(landed).toBeNull()
  })

  it('clicking an agent card starts a chat with that agent selected', async () => {
    renderHome()
    fireEvent.click(await screen.findByRole('button', { name: /research/ }))
    expect(landed?.pathname).toBe('/chat')
    expect(landed?.state?.agent).toBe('research')
  })

  it('the Memory shortcut navigates to the memory page', () => {
    renderHome()
    fireEvent.click(screen.getByRole('button', { name: 'Memory' }))
    expect(screen.getByText('memory page')).toBeTruthy()
  })

  it('the Analytics shortcut navigates to the analytics page', () => {
    renderHome()
    fireEvent.click(screen.getByRole('button', { name: 'Analytics' }))
    expect(screen.getByText('analytics page')).toBeTruthy()
  })

  it('carries picked #mention chips into the chat intent on send', async () => {
    vi.mocked(listKbCollections).mockResolvedValue([
      { id: '1', name: 'observability', description: '', doc_count: 0, chunk_count: 0, created_at: '', updated_at: '' },
    ])
    renderHome()
    const input = screen.getByLabelText('Message') as HTMLTextAreaElement

    fireEvent.change(input, { target: { value: '#obs' } })
    await screen.findByText('#observability')
    fireEvent.keyDown(input, { key: 'Enter' }) // selects the mention, adds the chip

    fireEvent.change(input, { target: { value: 'hello there' } })
    fireEvent.keyDown(input, { key: 'Enter' })

    expect(landed?.state?.knowledge).toEqual(['observability'])
    expect(landed?.state?.send).toBe('hello there')
  })
})
