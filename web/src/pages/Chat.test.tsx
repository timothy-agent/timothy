import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ChatStreamOptions } from '../api/client'
import type { ChatEvent, ChatRequest } from '../api/types'
import { Chat } from './Chat'

vi.mock('../api/client', () => ({
  ChatError: class ChatError extends Error {},
  chatStream: vi.fn(),
  retryStream: vi.fn(),
  getTranscript: vi.fn(),
  answerPermission: vi.fn(),
  listRoutes: vi.fn(),
  listAgents: vi.fn(),
}))

import { chatStream, getTranscript, listAgents, listRoutes } from '../api/client'

function renderChat() {
  return render(
    <MemoryRouter initialEntries={['/chat']}>
      <Routes>
        <Route path="/chat" element={<Chat onNeedToken={vi.fn()} />} />
        <Route path="/chat/:id" element={<Chat onNeedToken={vi.fn()} />} />
      </Routes>
    </MemoryRouter>,
  )
}

const routes = [
  { name: 'default', strategy: 'ordered', enabled: true, chain: [] },
  { name: 'summarize', strategy: 'price', enabled: true, chain: [] },
]

// Radix opens dropdowns on pointerdown, not click.
function openMenu(name: string) {
  const trigger = screen.getByRole('button', { name })
  fireEvent.pointerDown(trigger, { button: 0, pointerId: 1 })
  fireEvent.click(trigger)
}

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
  localStorage.clear()
  // jsdom lacks scrollIntoView; the message list calls it on update.
  Element.prototype.scrollIntoView = vi.fn()
  vi.mocked(listAgents).mockResolvedValue([])
  vi.mocked(listRoutes).mockResolvedValue(routes)
  vi.mocked(getTranscript).mockResolvedValue({
    session: { id: 's1', title: '', archived: false, created_at: '', updated_at: '' },
    items: [],
  })
  vi.mocked(chatStream).mockImplementation(
    async (_req: ChatRequest, onEvent: (ev: ChatEvent) => void, opts: ChatStreamOptions = {}) => {
      opts.onSession?.('s1')
      onEvent({ type: 'meta', session_id: 's1' })
    },
  )
})

describe('Chat route picker', () => {
  it('omits route from the request body when Auto is selected', async () => {
    renderChat()
    const input = screen.getByLabelText('Message')
    fireEvent.change(input, { target: { value: 'hello' } })
    fireEvent.keyDown(input, { key: 'Enter' })

    await waitFor(() => expect(chatStream).toHaveBeenCalled())
    const [req] = vi.mocked(chatStream).mock.calls[0]
    expect(req.message).toBe('hello')
    expect(req.route).toBeUndefined()
  })

  it('includes the chosen route in the request body', async () => {
    renderChat()

    await screen.findByRole('button', { name: 'Agent and route' })
    openMenu('Agent and route')
    fireEvent.click(await screen.findByText('summarize'))

    const input = screen.getByLabelText('Message')
    fireEvent.change(input, { target: { value: 'hello' } })
    fireEvent.keyDown(input, { key: 'Enter' })

    await waitFor(() => expect(chatStream).toHaveBeenCalled())
    const [req] = vi.mocked(chatStream).mock.calls[0]
    expect(req.route).toBe('summarize')
    expect(localStorage.getItem('timothy.route')).toBe('summarize')
  })
})
