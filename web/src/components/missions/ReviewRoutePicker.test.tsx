import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdminRoute, ExecutionPlanPhase, Mission } from '../../api/types'

vi.mock('../../api/client', () => ({
  listRoutes: vi.fn(),
  getMissionExecutionPlan: vi.fn(),
  patchMissionRouting: vi.fn(),
}))

import { getMissionExecutionPlan, listRoutes, patchMissionRouting } from '../../api/client'
import { ReviewRoutePicker } from './ReviewRoutePicker'

afterEach(cleanup)

const routes: AdminRoute[] = [
  { name: 'default', strategy: 'ordered', enabled: true, chain: [] },
  { name: 'careful', strategy: 'ordered', enabled: true, chain: [] },
  { name: 'disabled-route', strategy: 'ordered', enabled: false, chain: [] },
]

const paused = {
  id: 'm1',
  goal: 'g',
  kind: 'general',
  phase: 'prove',
  status: 'paused',
  pause_reason: 'infra',
  route: 'default',
  review_route: 'default',
  flow: 'full',
  plan: { units: [] },
  progress: [],
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
} as unknown as Mission

function provePhase(entries: ExecutionPlanPhase['entries']): ExecutionPlanPhase[] {
  return [
    {
      phase: 'prove',
      route: 'careful',
      route_source: 'explicit',
      axis: 'native',
      harness: '',
      harness_source: '',
      skipped: false,
      skip_reason: '',
      entries,
    },
  ]
}

const entry = {
  provider_name: 'OpenAI',
  driver: 'openaicompat',
  kind: 'chat',
  base_url: 'https://api.openai.com/v1',
  model: 'gpt-5',
  usable: true,
  skip_reason: '',
  selected: true,
}

describe('ReviewRoutePicker', () => {
  beforeEach(() => {
    // jsdom has no scrollIntoView; Radix Select calls it on open.
    Element.prototype.scrollIntoView = vi.fn()
    vi.mocked(listRoutes).mockResolvedValue(routes)
    vi.mocked(getMissionExecutionPlan).mockResolvedValue(provePhase([entry]))
    vi.mocked(patchMissionRouting).mockResolvedValue(undefined)
  })

  it('starts on the mission review route with Save disabled until something changes', async () => {
    render(<ReviewRoutePicker mission={paused} onSaved={vi.fn()} />)
    expect(await screen.findByRole('combobox', { name: 'Review route' })).toHaveTextContent('default')
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
  })

  it('saves the picked route through the routing endpoint', async () => {
    const onSaved = vi.fn()
    render(<ReviewRoutePicker mission={paused} onSaved={onSaved} />)
    fireEvent.click(await screen.findByRole('combobox', { name: 'Review route' }))
    fireEvent.click(await screen.findByRole('option', { name: 'careful' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Save' })).toBeEnabled())
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(patchMissionRouting).toHaveBeenCalledWith('m1', { review_route: 'careful', review_route_model: undefined }),
    )
    expect(onSaved).toHaveBeenCalled()
  })

  it('disables Save and warns when the picked route has no usable entry', async () => {
    vi.mocked(getMissionExecutionPlan).mockResolvedValue(
      provePhase([{ ...entry, usable: false, selected: false, skip_reason: 'cooling down until 12:00' }]),
    )
    render(<ReviewRoutePicker mission={paused} onSaved={vi.fn()} />)
    fireEvent.click(await screen.findByRole('combobox', { name: 'Review route' }))
    fireEvent.click(await screen.findByRole('option', { name: 'careful' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Route careful has no usable provider: cooling down until 12:00',
    )
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
    // The unusable entry stays listed in the model pin select, disabled
    // with its reason.
    fireEvent.click(screen.getByLabelText('Review model'))
    const option = await screen.findByText(/gpt-5 - cooling down until 12:00/)
    expect(option.closest('[role="option"]')).toHaveAttribute('aria-disabled', 'true')
  })
})
