import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const docs = vi.hoisted(() => ({ url: '' }))
vi.mock('./docsUrl', () => ({
  get DOCS_URL() {
    return docs.url
  },
}))
vi.mock('sonner', () => ({ toast: { error: vi.fn() } }))

import { OnboardingContext } from './context'
import { HelpMenu } from './HelpMenu'
import { onboardingState } from './testing'

function Where() {
  return <p data-testid="where">{useLocation().pathname}</p>
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

beforeEach(() => {
  docs.url = ''
})

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe('HelpMenu', () => {
  it('lists the help items', () => {
    renderMenu('/')
    for (const name of ['Setup checklist', "Restart this page's tour", 'Restart welcome', 'README on GitHub']) {
      expect(item(name)).toBeInTheDocument()
    }
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

  it('links the README when there is no docs site', () => {
    renderMenu('/')
    const link = item('README on GitHub')
    expect(link).toHaveAttribute('href', 'https://github.com/timothy-agent/timothy#readme')
    expect(link).toHaveAttribute('target', '_blank')
    expect(screen.queryByRole('menuitem', { name: 'Docs' })).toBeNull()
  })

  it('links the docs site when it is set', () => {
    docs.url = 'https://docs.example.test'
    renderMenu('/')
    expect(item('Docs')).toHaveAttribute('href', 'https://docs.example.test')
    expect(screen.queryByRole('menuitem', { name: 'README on GitHub' })).toBeNull()
  })
})
