import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ChatIntent } from './Home'
import { Home } from './Home'

vi.mock('../api/client', () => ({
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
    expect(screen.getByRole('button', { name: /Markets/ })).toBeTruthy()
  })

  it('submitting the composer starts a chat with the message', () => {
    renderHome()
    const input = screen.getByLabelText('Message')
    fireEvent.change(input, { target: { value: 'hello there' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(landed?.pathname).toBe('/chat')
    expect(landed?.state?.send).toBe('hello there')
    expect(landed?.state?.category).toBeTruthy()
  })

  it('an empty composer does not navigate', () => {
    renderHome()
    const input = screen.getByLabelText('Message')
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(landed).toBeNull()
  })

  it('skill tiles carry a prefill intent into chat', () => {
    renderHome()
    fireEvent.click(screen.getByRole('button', { name: /Travel/ }))
    expect(landed?.pathname).toBe('/chat')
    expect(landed?.state?.draft).toContain('travel-planning skill')
    expect(landed?.state?.send).toBeUndefined()
  })

  it('memory tiles navigate to the memory page', () => {
    renderHome()
    fireEvent.click(screen.getByRole('button', { name: /Queue/ }))
    expect(screen.getByText('memory page')).toBeTruthy()
  })
})
