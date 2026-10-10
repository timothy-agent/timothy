import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'

vi.mock('sonner', () => ({ toast: { error: vi.fn() } }))

import { OnboardingContext } from './context'
import { HelpMenu } from './HelpMenu'
import { onboardingState } from './testing'

function Where() {
  const loc = useLocation()
  return (
    <>
      <p data-testid="where">{loc.pathname}</p>
      <p data-testid="draft">{(loc.state as { draft?: string } | null)?.draft}</p>
    </>
  )
}

function renderMenu(path: string) {
  const updateProgress = vi.fn().mockResolvedValue(undefined)
  render(
    <MemoryRouter initialEntries={[path]}>
      <OnboardingContext.Provider value={{ ...onboardingState(), updateProgress }}>
        <HelpMenu />
        <Routes>
          <Route path="*" element={<Where />} />
        </Routes>
      </OnboardingContext.Provider>
    </MemoryRouter>,
  )
  fireEvent.pointerDown(screen.getByRole('button', { name: 'Help' }), { button: 0, pointerId: 1 })
  return updateProgress
}

const item = (name: string) => screen.getByRole('menuitem', { name })
const where = () => screen.getByTestId('where')

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe('HelpMenu', () => {
  it('lists the help items', () => {
    renderMenu('/')
    for (const name of ['Setup checklist', "Restart this page's tour", 'Restart welcome', 'Ask Timothy about this page', 'Docs']) {
      expect(item(name)).toBeInTheDocument()
    }
  })

  it('sizes the menu wider than its icon trigger so labels stay on one line', () => {
    renderMenu('/')
    expect(screen.getByRole('menu')).toHaveClass('w-60')
  })

  it("disables the tour restart on a page without a tour", () => {
    renderMenu('/')
    expect(item("Restart this page's tour")).toHaveAttribute('aria-disabled', 'true')
  })

  it('restarts the current page tour', () => {
    const onRestart = vi.fn()
    window.addEventListener('timothy:tour-restart', onRestart)
    const updateProgress = renderMenu('/memory')
    const restart = item("Restart this page's tour")
    expect(restart).not.toHaveAttribute('aria-disabled')
    fireEvent.click(restart)
    expect(updateProgress).toHaveBeenCalledWith({ tours_seen: { memory: 0 } })
    expect(onRestart).toHaveBeenCalledOnce()
    expect((onRestart.mock.calls[0][0] as CustomEvent).detail).toBe('memory')
    expect(where()).toHaveTextContent('/memory')
    window.removeEventListener('timothy:tour-restart', onRestart)
  })

  it('reopens the setup checklist on home', async () => {
    const updateProgress = renderMenu('/memory')
    fireEvent.click(item('Setup checklist'))
    expect(updateProgress).toHaveBeenCalledWith({ checklist_dismissed: false })
    await waitFor(() => expect(where()).toHaveTextContent(/^\/$/))
  })

  it('restarts the welcome wizard', async () => {
    const updateProgress = renderMenu('/memory')
    fireEvent.click(item('Restart welcome'))
    expect(updateProgress).toHaveBeenCalledWith({ wizard: 'pending' })
    await waitFor(() => expect(where()).toHaveTextContent('/welcome'))
  })

  it('opens chat with a draft naming the current page', async () => {
    renderMenu('/memory')
    fireEvent.click(item('Ask Timothy about this page'))
    await waitFor(() => expect(where()).toHaveTextContent('/chat'))
    expect(screen.getByTestId('draft')).toHaveTextContent('How do I use this page: /memory?')
  })

  it('links the docs site in a new tab', () => {
    renderMenu('/')
    const link = item('Docs')
    expect(link).toHaveAttribute('href', 'https://timothy-agent.github.io/docs/')
    expect(link).toHaveAttribute('target', '_blank')
  })
})
