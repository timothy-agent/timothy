import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { OnboardingProgress } from '../../api/types'
import { OnboardingContext } from '../context'
import { onboardingState } from '../testing'
import type { TourDef } from './types'
import { useTour } from './useTour'

afterEach(cleanup)

function demo(version = 1): TourDef {
  return {
    page: 'demo',
    version,
    steps: [
      { target: 'demo.a', title: 'A', body: 'First.' },
      { target: 'demo.missing', title: 'Missing', body: 'Never shown.' },
      { target: 'demo.b', title: 'B', body: 'Second.' },
    ],
  }
}

function Harness({ def, enabled }: { def: TourDef; enabled: boolean }) {
  const t = useTour(def, { enabled })
  return (
    <>
      <div data-tour="demo.a" />
      <div data-tour="demo.b" />
      <p data-testid="state">
        {t.active ? `${t.steps[t.stepIndex].title} ${t.stepIndex + 1}/${t.steps.length}` : 'off'}
      </p>
      <button onClick={t.next}>next</button>
      <button onClick={t.back}>back</button>
      <button onClick={t.restart}>restart</button>
    </>
  )
}

function renderTour({
  def = demo(),
  enabled = true,
  progress = {},
}: { def?: TourDef; enabled?: boolean; progress?: OnboardingProgress } = {}) {
  const updateProgress = vi.fn().mockResolvedValue(undefined)
  const wrap = (d: TourDef) => (
    <OnboardingContext.Provider value={{ ...onboardingState(), progress, updateProgress }}>
      <Harness def={d} enabled={enabled} />
    </OnboardingContext.Provider>
  )
  const utils = render(wrap(def))
  return { updateProgress, rerenderWith: (d: TourDef) => utils.rerender(wrap(d)) }
}

const state = () => screen.getByTestId('state')
const frame = () => act(() => new Promise<void>((r) => requestAnimationFrame(() => r())))
// The tour starts from a rAF callback outside act, so the effect that
// attaches the key listener can still be pending when the text lands.
async function shown(text: string) {
  await waitFor(() => expect(state()).toHaveTextContent(text))
  await act(async () => {})
}

describe('useTour', () => {
  it('starts when unseen and enabled, skipping steps with no anchor', async () => {
    renderTour()
    await waitFor(() => expect(state()).toHaveTextContent('A 1/2'))
  })

  it('skips steps whose anchor is hidden', async () => {
    const proto = Element.prototype as Element & { checkVisibility?: () => boolean }
    const original = proto.checkVisibility
    proto.checkVisibility = function (this: Element) {
      return this.getAttribute('data-tour') !== 'demo.b'
    }
    try {
      renderTour()
      await waitFor(() => expect(state()).toHaveTextContent('A 1/1'))
    } finally {
      proto.checkVisibility = original
    }
  })

  it('does not start when this version was seen', async () => {
    renderTour({ progress: { tours_seen: { demo: 1 } } })
    await frame()
    expect(state()).toHaveTextContent('off')
  })

  it('does not start when disabled', async () => {
    renderTour({ enabled: false })
    await frame()
    expect(state()).toHaveTextContent('off')
  })

  it('finishes on next from the last step and records the version', async () => {
    const { updateProgress } = renderTour()
    await waitFor(() => expect(state()).toHaveTextContent('A 1/2'))
    fireEvent.click(screen.getByText('next'))
    expect(state()).toHaveTextContent('B 2/2')
    fireEvent.click(screen.getByText('back'))
    expect(state()).toHaveTextContent('A 1/2')
    fireEvent.click(screen.getByText('next'))
    fireEvent.click(screen.getByText('next'))
    expect(state()).toHaveTextContent('off')
    expect(updateProgress).toHaveBeenCalledWith({ tours_seen: { demo: 1 } })
  })

  it('skips on Escape and records the version', async () => {
    const { updateProgress } = renderTour()
    await shown('A 1/2')
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(state()).toHaveTextContent('off')
    expect(updateProgress).toHaveBeenCalledWith({ tours_seen: { demo: 1 } })
  })

  it('moves with the arrow keys and Enter', async () => {
    renderTour()
    await shown('A 1/2')
    fireEvent.keyDown(window, { key: 'ArrowRight' })
    expect(state()).toHaveTextContent('B 2/2')
    fireEvent.keyDown(window, { key: 'ArrowLeft' })
    expect(state()).toHaveTextContent('A 1/2')
    fireEvent.keyDown(window, { key: 'Enter' })
    expect(state()).toHaveTextContent('B 2/2')
  })

  it('shows again after a version bump', async () => {
    const { rerenderWith } = renderTour({ progress: { tours_seen: { demo: 1 } } })
    await frame()
    expect(state()).toHaveTextContent('off')
    rerenderWith(demo(2))
    await waitFor(() => expect(state()).toHaveTextContent('A 1/2'))
  })

  it('restart reactivates a finished tour', async () => {
    renderTour()
    await shown('A 1/2')
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(state()).toHaveTextContent('off')
    fireEvent.click(screen.getByText('restart'))
    expect(state()).toHaveTextContent('A 1/2')
  })
})
