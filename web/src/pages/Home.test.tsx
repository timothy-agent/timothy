import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ChatIntent } from './Home'
import { Home } from './Home'

vi.mock('../api/client', () => ({
  listAgents: vi.fn().mockResolvedValue([]),
  listMemories: vi.fn(),
}))

import { listMemories } from '../api/client'

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
      </Routes>
    </MemoryRouter>,
  )
}

afterEach(cleanup)
beforeEach(() => {
  landed = null
  vi.clearAllMocks()
  vi.mocked(listMemories).mockResolvedValue([])
  localStorage.clear()
})

describe('Home', () => {
  it('shows the capability groups', () => {
    renderHome()
    for (const title of ['Chat', 'Memory', 'Skills', 'Workspace']) {
      expect(screen.getByRole('heading', { name: title })).toBeTruthy()
    }
    expect(screen.getByRole('button', { name: /New chat/ })).toBeTruthy()
    expect(screen.getByRole('button', { name: /Research/ })).toBeTruthy()
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

  it('an empty composer does not navigate', () => {
    renderHome()
    const input = screen.getByLabelText('Message')
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(landed).toBeNull()
  })

  it('the Research tile navigates to the dedicated research page', () => {
    renderHome()
    fireEvent.click(screen.getByRole('button', { name: /Research/ }))
    expect(screen.getByText('research page')).toBeTruthy()
  })

  it('memory tiles navigate to the memory page', () => {
    renderHome()
    fireEvent.click(screen.getByRole('button', { name: /Queue/ }))
    expect(screen.getByText('memory page')).toBeTruthy()
  })
})
