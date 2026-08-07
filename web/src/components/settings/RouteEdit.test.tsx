import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdminProvider, AdminRoute } from '../../api/types'
import { RouteEdit } from './RouteEdit'

vi.mock('../../api/client', () => ({
  listRoutes: vi.fn(),
  listProviders: vi.fn(),
  patchRoute: vi.fn(),
}))

import { listProviders, listRoutes, patchRoute } from '../../api/client'

const providers: AdminProvider[] = [
  {
    id: 'p1', name: 'anthropic', kind: 'api', driver: 'anthropic', base_url: '',
    default_model: 'sonnet', models: [{ id: 'sonnet' }], credential_ref: 'A_KEY',
    headers: {}, enabled: true,
  },
  {
    id: 'p2', name: 'grok', kind: 'api', driver: 'openaicompat', base_url: 'https://x.example/v1',
    default_model: 'grok-4', models: [{ id: 'grok-4' }], credential_ref: 'X_KEY',
    headers: {}, enabled: true,
  },
]

const orderedRoute: AdminRoute = {
  name: 'default',
  strategy: 'ordered',
  enabled: true,
  chain: [
    { provider_id: 'p1', model: 'sonnet' },
    { provider_id: 'p2', model: 'grok-4' },
  ],
  resolved: [
    {
      provider_id: 'p1', provider_name: 'anthropic', model: 'sonnet', usable: true,
      latency_ms: 812, uptime: 0.98, output_per_mtok: 15,
    },
    {
      provider_id: 'p2', provider_name: 'grok', model: 'grok-4', usable: false,
      skip_reason: 'unhealthy: credential X_KEY unresolved',
    },
  ],
  serving: { provider_id: 'p1', model: 'sonnet' },
}

const scoredRoute: AdminRoute = {
  ...orderedRoute,
  name: 'summarize',
  strategy: 'price',
  // Router order: grok (cheaper) first, opposite of the written chain.
  resolved: [
    {
      provider_id: 'p2', provider_name: 'grok', model: 'grok-4', usable: true,
      score: 0.92, norm_price: 1, output_per_mtok: 1,
    },
    {
      provider_id: 'p1', provider_name: 'anthropic', model: 'sonnet', usable: true,
      score: 0.056, norm_price: 0.04, output_per_mtok: 25,
    },
  ],
  serving: { provider_id: 'p2', model: 'grok-4' },
}

function renderRoute(name: string, override?: AdminRoute) {
  if (override) vi.mocked(listRoutes).mockResolvedValue([override, scoredRoute])
  return render(
    <MemoryRouter initialEntries={[`/settings/routes/${name}`]}>
      <Routes>
        <Route path="/settings/routes/:name" element={<RouteEdit />} />
      </Routes>
    </MemoryRouter>,
  )
}

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(listRoutes).mockResolvedValue([orderedRoute, scoredRoute])
  vi.mocked(listProviders).mockResolvedValue(providers)
  vi.mocked(patchRoute).mockResolvedValue(undefined)
  // jsdom lacks scrollIntoView; Radix Select calls it on open.
  Element.prototype.scrollIntoView = vi.fn()
})

describe('RouteEdit ordered pipeline', () => {
  it('shows the serving entry from the router, not a guess', async () => {
    renderRoute('default')
    await screen.findByTestId('pipeline')
    expect(screen.getByText('serving').closest('[data-testid="pipeline-card"]')).toHaveTextContent(
      'anthropic',
    )
  })

  it('surfaces the skip reason on an unusable entry', async () => {
    renderRoute('default')
    await screen.findByTestId('pipeline')
    expect(screen.getAllByText('unhealthy: credential X_KEY unresolved').length).toBeGreaterThan(0)
  })

  it('renders stats, and unpriced instead of $0', async () => {
    renderRoute('default')
    const cards = await screen.findAllByTestId('pipeline-card')
    expect(cards[0]).toHaveTextContent('812 ms')
    expect(cards[0]).toHaveTextContent('$15/MTok')
    expect(cards[0]).toHaveTextContent('98%')
    expect(cards[1]).toHaveTextContent('unpriced')
  })

  it('arrow button reorders via a single PATCH', async () => {
    renderRoute('default')
    await screen.findByTestId('pipeline')
    fireEvent.click(screen.getByRole('button', { name: 'Move sonnet right' }))
    await waitFor(() =>
      expect(patchRoute).toHaveBeenCalledWith('default', {
        chain: [
          { provider_id: 'p2', model: 'grok-4' },
          { provider_id: 'p1', model: 'sonnet' },
        ],
      }),
    )
    expect(patchRoute).toHaveBeenCalledTimes(1)
  })

  it('pointer drag commits one PATCH with the new order', async () => {
    renderRoute('default')
    const cards = await screen.findAllByTestId('pipeline-card')
    cards.forEach((card, i) => {
      const wrapper = card.parentElement as HTMLElement
      wrapper.getBoundingClientRect = () =>
        ({ left: i * 100, width: 100, right: i * 100 + 100, top: 0, bottom: 50, height: 50, x: i * 100, y: 0 }) as DOMRect
    })
    fireEvent.pointerDown(cards[0], { clientX: 10, button: 0 })
    fireEvent.pointerMove(window, { clientX: 180 })
    fireEvent.pointerUp(window)
    await waitFor(() =>
      expect(patchRoute).toHaveBeenCalledWith('default', {
        chain: [
          { provider_id: 'p2', model: 'grok-4' },
          { provider_id: 'p1', model: 'sonnet' },
        ],
      }),
    )
    expect(patchRoute).toHaveBeenCalledTimes(1)
  })

  it('Escape cancels a drag without a PATCH', async () => {
    renderRoute('default')
    const cards = await screen.findAllByTestId('pipeline-card')
    cards.forEach((card, i) => {
      const wrapper = card.parentElement as HTMLElement
      wrapper.getBoundingClientRect = () =>
        ({ left: i * 100, width: 100, right: i * 100 + 100, top: 0, bottom: 50, height: 50, x: i * 100, y: 0 }) as DOMRect
    })
    fireEvent.pointerDown(cards[0], { clientX: 10, button: 0 })
    fireEvent.pointerMove(window, { clientX: 180 })
    fireEvent.keyDown(window, { key: 'Escape' })
    fireEvent.pointerUp(window)
    expect(patchRoute).not.toHaveBeenCalled()
  })

  it('removes an entry', async () => {
    renderRoute('default')
    await screen.findByTestId('pipeline')
    fireEvent.click(screen.getByRole('button', { name: 'Remove grok-4' }))
    await waitFor(() =>
      expect(patchRoute).toHaveBeenCalledWith('default', {
        chain: [{ provider_id: 'p1', model: 'sonnet' }],
      }),
    )
  })
})

describe('RouteEdit harness', () => {
  it('shows a harness badge on a resolved chain entry with harness set', async () => {
    renderRoute('default', {
      ...orderedRoute,
      resolved: [
        { ...orderedRoute.resolved![0], harness: 'claude-cli' },
        orderedRoute.resolved![1],
      ],
    })
    const cards = await screen.findAllByTestId('pipeline-card')
    expect(cards[0]).toHaveTextContent('claude-cli')
    expect(cards[1]).not.toHaveTextContent('claude-cli')
  })

  it('disables Claude Code in the harness picker for a wire-incompatible provider', async () => {
    renderRoute('default')
    await screen.findByTestId('pipeline')
    fireEvent.click(screen.getByRole('combobox', { name: 'Provider' }))
    fireEvent.click(screen.getByRole('option', { name: 'grok' }))
    fireEvent.click(screen.getByRole('combobox', { name: 'Harness' }))
    expect(screen.getByRole('option', { name: 'Claude Code' })).toHaveAttribute('data-disabled')
  })

  it('leaves Claude Code enabled for an anthropic-driver provider', async () => {
    renderRoute('default')
    await screen.findByTestId('pipeline')
    fireEvent.click(screen.getByRole('combobox', { name: 'Provider' }))
    fireEvent.click(screen.getByRole('option', { name: 'anthropic' }))
    fireEvent.click(screen.getByRole('combobox', { name: 'Harness' }))
    expect(screen.getByRole('option', { name: 'Claude Code' })).not.toHaveAttribute('data-disabled')
  })
})

describe('RouteEdit scored pipeline', () => {
  it('renders the resolved order with score bars and no reorder controls', async () => {
    renderRoute('summarize')
    const cards = await screen.findAllByTestId('pipeline-card')
    // Router order (cheap grok first), not written chain order.
    expect(cards[0]).toHaveTextContent('grok')
    expect(cards[1]).toHaveTextContent('anthropic')
    expect(screen.getAllByTestId('score-fill')).toHaveLength(2)
    expect(screen.queryByRole('button', { name: /Move .* (left|right)/ })).toBeNull()
    expect(screen.getByText('auto-sorted by score')).toBeInTheDocument()
  })

  it('serving chip follows the router order', async () => {
    renderRoute('summarize')
    await screen.findByTestId('pipeline')
    expect(screen.getByText('serving').closest('[data-testid="pipeline-card"]')).toHaveTextContent(
      'grok',
    )
  })
})
