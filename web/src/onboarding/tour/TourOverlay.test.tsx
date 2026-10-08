import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { OnboardingContext } from '../context'
import { onboardingState } from '../testing'
import { TourOverlay } from './TourOverlay'
import type { TourDef } from './types'
import { useTour } from './useTour'

const def: TourDef = {
  page: 'demo',
  version: 1,
  steps: [
    { target: 'demo.a', title: 'First thing', body: 'Look here.' },
    { target: 'demo.b', title: 'Second thing', body: 'Then here.' },
  ],
}

function Page() {
  const tour = useTour(def, { enabled: true })
  return (
    <>
      <button data-tour="demo.a">a</button>
      <button data-tour="demo.b">b</button>
      <TourOverlay {...tour} />
    </>
  )
}

beforeEach(() => {
  Element.prototype.scrollIntoView = vi.fn()
})
afterEach(cleanup)

async function renderPage() {
  render(
    <OnboardingContext.Provider value={{ ...onboardingState(), progress: {} }}>
      <Page />
    </OnboardingContext.Provider>,
  )
  return screen.findByRole('dialog', { name: 'First thing' })
}

describe('TourOverlay', () => {
  it('shows the first step with its count and a mask', async () => {
    const dialog = await renderPage()
    expect(dialog).toHaveTextContent('Look here.')
    expect(dialog).toHaveTextContent('Step 1 of 2')
    expect(screen.getByTestId('tour-mask')).toBeInTheDocument()
  })

  it('hides Back on the first step', async () => {
    await renderPage()
    expect(screen.queryByRole('button', { name: 'Back' })).not.toBeInTheDocument()
  })

  it('focuses Next when it opens', async () => {
    await renderPage()
    await waitFor(() => expect(screen.getByRole('button', { name: 'Next' })).toHaveFocus())
  })

  it('advances with Next and ends with Done', async () => {
    await renderPage()
    fireEvent.click(screen.getByRole('button', { name: 'Next' }))
    const dialog = await screen.findByRole('dialog', { name: 'Second thing' })
    expect(dialog).toHaveTextContent('Step 2 of 2')
    expect(screen.getByRole('button', { name: 'Back' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Done' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(screen.queryByTestId('tour-mask')).not.toBeInTheDocument()
  })
})
