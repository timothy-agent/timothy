import { cleanup, render, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { acknowledgeNeedToken } from '../../api/client'
import App from '../../App'
import type { TourDef } from '../tour/types'
import { chatTour } from './chat'
import { missionNewTour } from './missionNew'
import { missionsTour } from './missions'

// Every tour marked seen: these tests pin anchors, not the overlay.
vi.mock('../context', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../context')>()
  const { onboardingState } = await import('../testing')
  return { ...actual, useOnboarding: () => onboardingState() }
})

// Every network call fails harmlessly: the anchors are static.
function fakeFetch() {
  return Promise.reject(new Error('no network in tests'))
}

beforeEach(() => {
  localStorage.setItem('timothy.token', 'test-token')
  Element.prototype.scrollIntoView = vi.fn()
  vi.stubGlobal('fetch', vi.fn(fakeFetch))
})

afterEach(() => {
  cleanup()
  localStorage.clear()
  vi.unstubAllGlobals()
  acknowledgeNeedToken()
})

const cases: [string, TourDef][] = [
  ['/chat', chatTour],
  ['/missions', missionsTour],
  ['/missions/new', missionNewTour],
]

describe('tour anchors', () => {
  it.each(cases)('every %s step has its anchor on the page', async (path, def) => {
    render(
      <MemoryRouter initialEntries={[path]}>
        <App />
      </MemoryRouter>,
    )
    for (const step of def.steps) {
      await waitFor(() => expect(document.querySelector(`[data-tour="${step.target}"]`)).not.toBeNull())
    }
  })
})
