import axe from 'axe-core'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdminProvider, AdminRoute } from '../../api/types'
import { TooltipProvider } from '../ui/tooltip'
import { RouteEdit } from './RouteEdit'

vi.mock('../../api/client', () => ({
  catalogModelsForProvider: vi.fn(),
  listRoutes: vi.fn(),
  listProviders: vi.fn(),
  patchRoute: vi.fn(),
}))
vi.mock('sonner', () => ({ toast: { error: vi.fn(), success: vi.fn() } }))

import { catalogModelsForProvider, listProviders, listRoutes, patchRoute } from '../../api/client'
import { toast } from 'sonner'

const providers: AdminProvider[] = [
  {
    id: 'p1', name: 'anthropic', kind: 'api', driver: 'anthropic', base_url: '',
    default_model: 'sonnet', credential_ref: 'A_KEY',
    headers: {}, enabled: true,
  },
  {
    id: 'p2', name: 'grok', kind: 'api', driver: 'openaicompat', base_url: 'https://x.example/v1',
    default_model: 'grok-4', credential_ref: 'X_KEY',
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
    <TooltipProvider>
      <MemoryRouter initialEntries={[`/settings/routes/${name}`]}>
        <Routes>
          <Route path="/settings/routes/:name" element={<RouteEdit />} />
        </Routes>
      </MemoryRouter>
    </TooltipProvider>,
  )
}

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(listRoutes).mockResolvedValue([orderedRoute, scoredRoute])
  vi.mocked(listProviders).mockResolvedValue(providers)
  vi.mocked(patchRoute).mockResolvedValue(undefined)
  vi.mocked(catalogModelsForProvider).mockResolvedValue([])
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
    expect(cards[0]).toHaveTextContent('out $15')
    expect(cards[0]).toHaveTextContent('98%')
    expect(cards[1]).toHaveTextContent('unpriced')
  })

  it('arrow reorder stages, Save sends one PATCH with the order', async () => {
    renderRoute('default')
    await screen.findByTestId('pipeline')
    fireEvent.click(screen.getByRole('button', { name: 'Move sonnet right' }))
    expect(patchRoute).not.toHaveBeenCalled()
    expect(await screen.findByText('Unsaved changes')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(patchRoute).toHaveBeenCalledWith('default', {
        strategy: 'ordered',
        enabled: true,
        chain: [
          { provider_id: 'p2', model: 'grok-4' },
          { provider_id: 'p1', model: 'sonnet' },
        ],
      }),
    )
    expect(patchRoute).toHaveBeenCalledTimes(1)
  })

  it('drag stages, Save sends one PATCH with the new order', async () => {
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
    expect(patchRoute).not.toHaveBeenCalled()
    expect(await screen.findByText('Unsaved changes')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(patchRoute).toHaveBeenCalledWith('default', {
        strategy: 'ordered',
        enabled: true,
        chain: [
          { provider_id: 'p2', model: 'grok-4' },
          { provider_id: 'p1', model: 'sonnet' },
        ],
      }),
    )
    expect(patchRoute).toHaveBeenCalledTimes(1)
  })

  it('Escape cancels a drag without a PATCH, order and dirty state unchanged', async () => {
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
    expect(screen.queryByText('Unsaved changes')).not.toBeInTheDocument()
    const settled = await screen.findAllByTestId('pipeline-card')
    expect(settled[0]).toHaveTextContent('anthropic')
    expect(settled[1]).toHaveTextContent('grok')
  })

  it('removes an entry: stages, Save sends the PATCH', async () => {
    renderRoute('default')
    await screen.findByTestId('pipeline')
    fireEvent.click(screen.getByRole('button', { name: 'Remove grok-4' }))
    expect(patchRoute).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(patchRoute).toHaveBeenCalledWith('default', {
        strategy: 'ordered',
        enabled: true,
        chain: [{ provider_id: 'p1', model: 'sonnet' }],
      }),
    )
    expect(patchRoute).toHaveBeenCalledTimes(1)
  })

  it('strategy and Enabled stage; Save sends one PATCH; Cancel restores the server chain', async () => {
    renderRoute('default')
    await screen.findByTestId('pipeline')

    fireEvent.click(screen.getByRole('combobox', { name: 'default strategy' }))
    fireEvent.click(await screen.findByRole('option', { name: 'Cheapest' }))
    fireEvent.click(screen.getByRole('switch', { name: 'default route enabled' }))
    expect(patchRoute).not.toHaveBeenCalled()
    expect(await screen.findByText('Unsaved changes')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(patchRoute).not.toHaveBeenCalled()
    expect(screen.queryByText('Unsaved changes')).not.toBeInTheDocument()
    expect(screen.getByRole('switch', { name: 'default route enabled' })).toBeChecked()
  })

  it('zero PATCH before Save; a failed Save keeps state and renders an Alert with Retry', async () => {
    vi.mocked(patchRoute).mockRejectedValueOnce(new Error('network down'))
    renderRoute('default')
    await screen.findByTestId('pipeline')
    fireEvent.click(screen.getByRole('button', { name: 'Move sonnet right' }))
    expect(patchRoute).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('network down')
    expect(screen.getByText('Unsaved changes')).toBeInTheDocument()

    vi.mocked(patchRoute).mockResolvedValueOnce(undefined)
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(patchRoute).toHaveBeenCalledTimes(2))
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

  it('removes a duplicate resolved entry with no matching chain reference, falling back to a provider/model filter', async () => {
    // Two resolved rows both point at p1/sonnet: the first consumes the
    // one matching chain entry from the pool by splice, so the second
    // display entry is a synthesized object with no chain reference.
    // Removing that second card exercises removeEntry's fallback (its
    // indexOf(target) misses, so it filters by provider_id/model
    // instead of array index) and actually changes the staged chain.
    const dupResolvedRoute: AdminRoute = {
      ...scoredRoute,
      name: 'dup',
      chain: [{ provider_id: 'p1', model: 'sonnet' }],
      resolved: [
        { provider_id: 'p1', provider_name: 'anthropic', model: 'sonnet', usable: true, score: 0.5 },
        { provider_id: 'p1', provider_name: 'anthropic', model: 'sonnet', usable: true, score: 0.4 },
      ],
      serving: { provider_id: 'p1', model: 'sonnet' },
    }
    vi.mocked(listRoutes).mockResolvedValue([orderedRoute, dupResolvedRoute])
    renderRoute('dup')
    const cards = await screen.findAllByTestId('pipeline-card')
    expect(cards).toHaveLength(2)

    const removeButtons = screen.getAllByRole('button', { name: 'Remove sonnet' })
    fireEvent.click(removeButtons[1])
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() =>
      expect(patchRoute).toHaveBeenCalledWith('dup', {
        strategy: 'price',
        enabled: true,
        chain: [],
      }),
    )
  })
})

describe('RouteEdit add-chain-entry provider and model pickers', () => {
  it('shows each provider option with its name and brand icon', async () => {
    renderRoute('default')
    fireEvent.click(await screen.findByRole('combobox', { name: 'Provider' }))

    const anthropicOption = await screen.findByRole('option', { name: /anthropic/ })
    expect(anthropicOption).toBeInTheDocument()
    expect(anthropicOption.querySelector('svg')).toBeInTheDocument()
    expect(screen.getByRole('option', { name: /grok/ })).toBeInTheDocument()
  })

  it('lists catalog models with prices', async () => {
    vi.mocked(catalogModelsForProvider).mockResolvedValue([
      { id: 'sonnet', model_key: 'sonnet', litellm_provider: 'anthropic', mode: 'chat' },
      {
        id: 'claude-opus-4-6',
        model_key: 'claude-opus-4-6',
        litellm_provider: 'anthropic',
        mode: 'chat',
        input_per_mtok: 15,
        output_per_mtok: 75,
      },
    ])
    renderRoute('default')
    fireEvent.click(await screen.findByRole('combobox', { name: 'Provider' }))
    fireEvent.click(await screen.findByRole('option', { name: /anthropic/ }))

    const modelInput = await screen.findByRole('textbox', { name: 'Model' })
    fireEvent.change(modelInput, { target: { value: '' } })
    fireEvent.focus(modelInput)

    expect(await screen.findByRole('option', { name: /^sonnet/ })).toBeInTheDocument()
    expect(
      await screen.findByRole('option', { name: /claude-opus-4-6.*in \$15 · out \$75 \/MTok/ }),
    ).toBeInTheDocument()
  })

  it('debounces typing into a single catalog search call with the typed q', async () => {
    vi.mocked(catalogModelsForProvider).mockResolvedValue([])
    renderRoute('default')
    fireEvent.click(await screen.findByRole('combobox', { name: 'Provider' }))
    fireEvent.click(await screen.findByRole('option', { name: /anthropic/ }))

    const modelInput = await screen.findByRole('textbox', { name: 'Model' })
    fireEvent.change(modelInput, { target: { value: 'claude-op' } })
    fireEvent.change(modelInput, { target: { value: 'claude-opus' } })

    await waitFor(() => expect(catalogModelsForProvider).toHaveBeenCalledWith('p1', 'claude-opus'))
    expect(catalogModelsForProvider).not.toHaveBeenCalledWith('p1', 'claude-op')
  })

  it('picking a suggested model and adding it stages the chain entry; Save sends it', async () => {
    vi.mocked(catalogModelsForProvider).mockResolvedValue([
      {
        id: 'claude-opus-4-6',
        model_key: 'claude-opus-4-6',
        litellm_provider: 'anthropic',
        mode: 'chat',
        input_per_mtok: 15,
        output_per_mtok: 75,
      },
    ])
    renderRoute('default')
    fireEvent.click(await screen.findByRole('combobox', { name: 'Provider' }))
    fireEvent.click(await screen.findByRole('option', { name: /anthropic/ }))

    const modelInput = await screen.findByRole('textbox', { name: 'Model' })
    fireEvent.change(modelInput, { target: { value: '' } })
    fireEvent.focus(modelInput)
    fireEvent.click(await screen.findByRole('option', { name: /^claude-opus-4-6/ }))

    fireEvent.click(screen.getByRole('button', { name: 'Add' }))
    expect(patchRoute).not.toHaveBeenCalled()
    expect(await screen.findByText('Unsaved changes')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(patchRoute).toHaveBeenCalledWith('default', {
        strategy: 'ordered',
        enabled: true,
        chain: [
          { provider_id: 'p1', model: 'sonnet' },
          { provider_id: 'p2', model: 'grok-4' },
          { provider_id: 'p1', model: 'claude-opus-4-6' },
        ],
      }),
    )
    expect(patchRoute).toHaveBeenCalledTimes(1)
  })
})

describe('RouteEdit load error', () => {
  it('shows a toast when loading the route fails', async () => {
    vi.mocked(listRoutes).mockRejectedValue(new Error('network down'))
    renderRoute('default')

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Could not load route', { description: 'network down' }),
    )
  })
})

describe('RouteEdit accessibility', () => {
  it('has no axe violations', async () => {
    const { container } = renderRoute('default')
    await screen.findByTestId('pipeline')
    const results = await axe.run(container, { rules: { 'color-contrast': { enabled: false } } })
    expect(results.violations).toEqual([])
  })
})
