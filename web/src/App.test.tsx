import { cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { acknowledgeNeedToken } from './api/client'
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
  acknowledgeNeedToken()
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

  it('lists Automations directly below Missions', async () => {
    renderAt('/')
    const links = await screen.findAllByRole('link')
    const labels = links.map((l) => l.textContent).filter((t) => t === 'Missions' || t === 'Automations')
    expect(labels).toEqual(['Missions', 'Automations'])
  })
})

// The footer's theme button picks its glyph from a map keyed on the
// current theme. lucide stamps a `lucide-<kebab-name>` class on every
// glyph, so asserting on it pins the mapping.
describe('Theme toggle icon', () => {
  it('shows the moon glyph on a dark theme', () => {
    localStorage.setItem('timothy.theme', 'dark')
    const { container } = renderAt('/')
    expect(container.querySelector('.lucide-moon')).toBeInTheDocument()
    expect(container.querySelector('.lucide-sun')).toBeNull()
  })

  it('shows the sun glyph on a light theme', () => {
    localStorage.setItem('timothy.theme', 'light')
    const { container } = renderAt('/')
    expect(container.querySelector('.lucide-sun')).toBeInTheDocument()
    expect(container.querySelector('.lucide-moon')).toBeNull()
  })

  it('swaps the glyph when the theme is cycled', () => {
    localStorage.setItem('timothy.theme', 'light')
    const { container } = renderAt('/')
    expect(container.querySelector('.lucide-sun')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Light theme' }))
    expect(container.querySelector('.lucide-moon')).toBeInTheDocument()
  })
})

describe('Legacy edit schedule route', () => {
  it('redirects /missions/schedules/:id/edit to /automations/:id/edit', async () => {
    renderAt('/missions/schedules/s1/edit')
    // No GET-by-id for schedules: the redirected EditSchedule page can't
    // resolve 's1' against an empty (failed-fetch) schedule list, so it
    // falls back to its own not-found copy — proof the route landed.
    expect(await screen.findByText('Schedule not found.')).toBeTruthy()
  })
})

describe('Settings sidebar submenu', () => {
  it('starts expanded and highlights the active area on a settings route', async () => {
    renderAt('/settings/secrets')
    const sidebar = document.querySelector('[data-sidebar="sidebar"]') as HTMLElement
    const providersLink = await within(sidebar).findByRole('link', { name: 'Providers' })
    const secretsLink = within(sidebar).getByRole('link', { name: 'Secrets' })
    expect(secretsLink.getAttribute('data-active')).toBe('true')
    expect(providersLink.getAttribute('data-active')).toBe('false')
  })

  it('collapses and expands on click without navigating away', async () => {
    renderAt('/settings/providers')
    const sidebar = document.querySelector('[data-sidebar="sidebar"]') as HTMLElement
    await within(sidebar).findByRole('link', { name: 'Providers' })
    const settingsButton = within(sidebar).getByRole('button', { name: 'Settings' })
    expect(settingsButton.getAttribute('aria-expanded')).toBe('true')

    fireEvent.click(settingsButton)
    expect(within(sidebar).queryByRole('link', { name: 'Providers' })).toBeNull()
    expect(settingsButton.getAttribute('aria-expanded')).toBe('false')

    fireEvent.click(settingsButton)
    expect(await within(sidebar).findByRole('link', { name: 'Providers' })).toBeTruthy()
    expect(settingsButton.getAttribute('aria-expanded')).toBe('true')
  })

  it('is collapsed by default off a settings route', () => {
    renderAt('/memory')
    const sidebar = document.querySelector('[data-sidebar="sidebar"]') as HTMLElement
    expect(within(sidebar).queryByRole('link', { name: 'Providers' })).toBeNull()
  })
})

describe('API token dialog', () => {
  it('opens when a request is unauthorized', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ error: 'unauthorized', message: 'missing or invalid bearer token' }), {
          status: 401,
          headers: { 'Content-Type': 'application/json' },
        }),
      ),
    )
    renderAt('/settings/providers')
    expect(await screen.findByLabelText('API token')).toBeTruthy()
    expect(screen.getByText(/not an LLM provider API key/)).toBeTruthy()
  })
})
