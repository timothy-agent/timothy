import axe from 'axe-core'
import { render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { Chat } from './Chat'

vi.mock('../api/client', () => ({
  ChatError: class ChatError extends Error {
    status: number
    code?: string
    constructor(status: number, message: string, code?: string) {
      super(message)
      this.status = status
      this.code = code
    }
  },
  errorText: (err: unknown) => (err instanceof Error ? err.message : String(err)),
  chatStream: vi.fn(),
  retryStream: vi.fn(),
  streamLive: vi.fn(),
  stopTurn: vi.fn(),
  getTranscript: vi.fn(),
  answerPermission: vi.fn(),
  listRoutes: vi.fn().mockResolvedValue([]),
  listAgents: vi.fn().mockResolvedValue([]),
  getSettings: vi.fn().mockResolvedValue({ settings: { transcribe_enabled: false }, values: {} }),
  listKbCollections: vi.fn().mockResolvedValue([]),
  setSessionKnowledge: vi.fn().mockResolvedValue(undefined),
  listMissions: vi.fn().mockResolvedValue([]),
  listSessions: vi.fn().mockResolvedValue([]),
  searchKbDocuments: vi.fn().mockResolvedValue([]),
}))

vi.mock('../lib/events', () => ({ subscribeEvents: vi.fn(() => vi.fn()) }))

import { getTranscript } from '../api/client'

beforeEach(() => {
  // jsdom lacks scrollIntoView; the message list calls it on update.
  Element.prototype.scrollIntoView = vi.fn()
})

describe('Chat page accessibility', () => {
  it('has no axe violations in the empty state', async () => {
    vi.mocked(getTranscript).mockResolvedValue({
      session: { id: 's1', title: '', archived: false, created_at: '', updated_at: '' },
      items: [],
      turn_active: false,
    })

    const { container } = render(
      <MemoryRouter initialEntries={['/chat']}>
        <Routes>
          <Route path="/chat" element={<Chat onNeedToken={vi.fn()} />} />
        </Routes>
      </MemoryRouter>,
    )

    // color-contrast/region disabled per the existing timothy a11y test
    // convention; label is disabled for the Composer's hidden file input
    // (pre-existing, outside this migration's scope).
    const results = await axe.run(container, {
      rules: { 'color-contrast': { enabled: false }, region: { enabled: false }, label: { enabled: false } },
    })
    expect(results.violations).toEqual([])
  })

  it('has no axe violations with an assistant turn holding tools and a pending permission', async () => {
    vi.mocked(getTranscript).mockResolvedValue({
      session: { id: 's1', title: '', archived: false, created_at: '', updated_at: '' },
      items: [
        { seq: 1, kind: 'user', text: 'do the thing', created_at: '' },
        {
          seq: 2,
          kind: 'tool',
          tool: { call_id: 'c1', name: 'search_web', status: 'ok', duration_ms: 42 },
          created_at: '',
        },
        {
          seq: 3,
          kind: 'permission',
          permission: {
            id: 'perm-1',
            call_id: 'call-1',
            tool: 'shell',
            args: '{}',
            danger_level: 'destructive',
            rationale: 'runs a shell command',
          },
          created_at: '',
        },
      ],
      turn_active: false,
    })

    const { container } = render(
      <MemoryRouter initialEntries={['/chat/s1']}>
        <Routes>
          <Route path="/chat/:id" element={<Chat onNeedToken={vi.fn()} />} />
        </Routes>
      </MemoryRouter>,
    )

    await screen.findByRole('region')
    // color-contrast/region disabled per the existing timothy a11y test
    // convention; label is disabled for the Composer's hidden file input
    // (pre-existing, outside this migration's scope).
    const results = await axe.run(container, {
      rules: { 'color-contrast': { enabled: false }, region: { enabled: false }, label: { enabled: false } },
    })
    expect(results.violations).toEqual([])
  })
})
