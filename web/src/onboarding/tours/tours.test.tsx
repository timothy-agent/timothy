import { cleanup, render, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { acknowledgeNeedToken } from '../../api/client'
import App from '../../App'
import type { TourDef } from '../tour/types'
import { analyticsTour } from './analytics'
import { automationsTour } from './automations'
import { chatTour } from './chat'
import { knowledgeTour } from './knowledge'
import { memoryTour } from './memory'
import { missionNewTour } from './missionNew'
import { missionsTour } from './missions'
import { settingsTour } from './settings'

// Every tour marked seen: these tests pin anchors, not the overlay.
vi.mock('../context', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../context')>()
  const { onboardingState } = await import('../testing')
  return { ...actual, useOnboarding: () => onboardingState() }
})

// jsdom has no canvas for ECharts.
vi.mock('../../components/charts/EChart', () => ({ EChart: () => null }))

// Every network call fails harmlessly, except one automation template so
// the template gallery renders its anchor.
function fakeFetch(input: RequestInfo | URL) {
  if (String(input).includes('/v1/automations/templates')) {
    const template = { id: 't1', name: 'Digest', description: 'Daily digest.', icon: 'mail', triggers: [], requires: [], missing: [] }
    return Promise.resolve(new Response(JSON.stringify({ templates: [template] })))
  }
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
  ['/automations', automationsTour],
  ['/knowledge', knowledgeTour],
  ['/memory', memoryTour],
  ['/analytics', analyticsTour],
  // The submenu starts open on a settings path.
  ['/settings/providers', settingsTour],
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

describe('tour copy', () => {
  const body = (def: TourDef, target: string) => def.steps.find((s) => s.target === target)?.body ?? ''

  it('names only what the app offers', () => {
    expect(body(chatTour, 'chat.composer')).not.toContain('@')
    expect(body(chatTour, 'chat.composer')).toContain('mission, chat or document')
    expect(body(automationsTour, 'automations.new')).not.toContain('email')
    expect(body(settingsTour, 'settings.destinations')).not.toContain('file')
    expect(body(settingsTour, 'settings.channels')).toContain('Slack')
  })

  it.each(cases)('%s tour has no em dash', (_path, def) => {
    for (const step of def.steps) expect(`${step.title} ${step.body}`).not.toContain('—')
  })
})
