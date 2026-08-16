import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import App from './App'

// App wires in a lot of background polling (sessions, pending memories,
// pending permissions, the /v1/events stream) — none of it relevant to
// the sidebar's Settings submenu, so every fetch is stubbed to fail
// harmlessly rather than mocking each api/client call individually.
beforeEach(() => {
  localStorage.setItem('timothy.token', 'test-token')
  vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('no network in tests')))
  // jsdom has no matchMedia; the sidebar's mobile breakpoint hook and
  // sonner's Toaster both call it on mount.
  vi.stubGlobal(
    'matchMedia',
    vi.fn().mockReturnValue({
      matches: false,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }),
  )
})

afterEach(() => {
  cleanup()
  localStorage.clear()
  vi.unstubAllGlobals()
})

function renderAt(initialEntry: string) {
  return render(
    <MemoryRouter initialEntries={[initialEntry]}>
      <App />
    </MemoryRouter>,
  )
}

describe('Sidebar nav', () => {
  it('lists Knowledge directly above Memory', async () => {
    renderAt('/')
    const links = await screen.findAllByRole('link')
    const labels = links.map((l) => l.textContent).filter((t) => t === 'Knowledge' || t === 'Memory')
    expect(labels).toEqual(['Knowledge', 'Memory'])
  })
})

describe('Settings sidebar submenu', () => {
  it('starts expanded and highlights the active area on a settings route', async () => {
    renderAt('/settings/secrets')
    const providersLink = await screen.findByRole('link', { name: 'Providers' })
    const secretsLink = screen.getByRole('link', { name: 'Secrets' })
    expect(secretsLink.getAttribute('data-active')).toBe('true')
    expect(providersLink.getAttribute('data-active')).toBe('false')
  })

  it('collapses and expands on click without navigating away', async () => {
    renderAt('/settings/providers')
    await screen.findByRole('link', { name: 'Providers' })

    fireEvent.click(screen.getByRole('button', { name: 'Settings' }))
    expect(screen.queryByRole('link', { name: 'Providers' })).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: 'Settings' }))
    expect(await screen.findByRole('link', { name: 'Providers' })).toBeTruthy()
  })

  it('is collapsed by default off a settings route', () => {
    renderAt('/memory')
    expect(screen.queryByRole('link', { name: 'Providers' })).toBeNull()
  })
})
