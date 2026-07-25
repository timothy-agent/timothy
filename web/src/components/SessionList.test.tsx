import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { SessionMeta } from '../api/types'
import { SessionsContext } from '../lib/sessions'
import { SessionList } from './SessionList'
import { SidebarProvider } from './ui/sidebar'

vi.mock('../api/client', () => ({
  deleteSession: vi.fn(),
  updateSession: vi.fn(),
}))

// SidebarProvider's useIsMobile needs matchMedia, which jsdom lacks.
window.matchMedia = (query: string) =>
  ({
    matches: false,
    media: query,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    onchange: null,
    dispatchEvent: () => false,
  }) as MediaQueryList

import { deleteSession } from '../api/client'

const meta: SessionMeta = {
  id: '11111111-1111-1111-1111-111111111111',
  title: 'doomed chat',
  archived: false,
  created_at: '2026-07-25T10:00:00Z',
  updated_at: '2026-07-25T10:00:00Z',
}

const refresh = vi.fn()

function renderList() {
  return render(
    <MemoryRouter>
      <SidebarProvider>
        <SessionsContext.Provider
          value={{
            sessions: [meta],
            query: '',
            setQuery: () => {},
            refresh,
            hasMore: false,
            loadMore: () => {},
          }}
        >
          <SessionList />
        </SessionsContext.Provider>
      </SidebarProvider>
    </MemoryRouter>,
  )
}

// Radix opens dropdowns on pointerdown, not click.
function openMenu() {
  const trigger = screen.getByRole('button', { name: /Actions for doomed chat/ })
  fireEvent.pointerDown(trigger, { button: 0, pointerId: 1 })
  fireEvent.click(trigger)
}

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(deleteSession).mockResolvedValue(undefined)
})

describe('SessionList delete', () => {
  it('confirms before deleting, then refreshes', async () => {
    renderList()
    openMenu()
    fireEvent.click(await screen.findByText('Delete'))
    expect(await screen.findByText(/permanently removes the conversation/)).toBeInTheDocument()
    expect(deleteSession).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    await waitFor(() => expect(deleteSession).toHaveBeenCalledWith(meta.id))
    expect(refresh).toHaveBeenCalled()
  })

  it('cancel closes the dialog without deleting', async () => {
    renderList()
    openMenu()
    fireEvent.click(await screen.findByText('Delete'))
    fireEvent.click(await screen.findByRole('button', { name: 'Cancel' }))
    await waitFor(() =>
      expect(screen.queryByText(/permanently removes the conversation/)).not.toBeInTheDocument(),
    )
    expect(deleteSession).not.toHaveBeenCalled()
  })
})
