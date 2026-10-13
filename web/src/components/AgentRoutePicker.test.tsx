import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdminAgent, AdminProvider, AdminRoute, AvailableModel } from '../api/types'

vi.mock('../api/client', () => ({
  listAgents: vi.fn(),
  listRoutes: vi.fn(),
  listProviders: vi.fn(),
  availableModels: vi.fn(),
}))

const agents: AdminAgent[] = [
  {
    id: '1',
    name: 'general',
    description: '',
    prompt_overlay: '',
    route: '',
    skills: [],
    tools: [],
    memory: false,
    is_default: true,
    enabled: true,
    review_route: '',
  },
  {
    id: '2',
    name: 'researcher',
    description: '',
    prompt_overlay: '',
    route: '',
    skills: [],
    tools: [],
    memory: false,
    is_default: false,
    enabled: true,
    review_route: '',
  },
]

const routes: AdminRoute[] = [
  {
    name: 'default',
    strategy: 'ordered',
    enabled: true,
    chain: [
      { provider_id: 'p1', model: 'glm-5.2' },
      { provider_id: 'p2', model: 'nova-pro' },
    ],
  },
  { name: 'local', strategy: 'price', enabled: true, chain: [] },
]

const providers: AdminProvider[] = [
  {
    id: 'p1',
    name: 'Anthropic',
    kind: 'api',
    driver: 'anthropic',
    base_url: '',
    default_model: 'sonnet',
    credential_ref: 'A',
    headers: {},
    enabled: true,
  },
]

const models: AvailableModel[] = [{ id: 'sonnet' }, { id: 'opus' }]

// Radix opens popovers on pointerdown, not click.
function openMenu(name: string) {
  const trigger = screen.getByRole('button', { name })
  fireEvent.pointerDown(trigger, { button: 0, pointerId: 1 })
  fireEvent.click(trigger)
}

function openPanel(title: string) {
  fireEvent.click(screen.getByRole('button', { name: title }))
}

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
})

// AgentRoutePicker composes AgentPicker's useAgents/useRoutes/
// useProviderModels, all module-level caches — each test resets modules
// and re-imports fresh to avoid one test's fetch leaking into the next.
async function freshPicker() {
  vi.resetModules()
  const client = await import('../api/client')
  const picker = await import('./AgentPicker')
  picker.resetProviderModelsCache()
  const { AgentRoutePicker } = await import('./AgentRoutePicker')
  vi.mocked(client.listProviders).mockResolvedValue(providers)
  vi.mocked(client.availableModels).mockResolvedValue(models)
  return {
    AgentRoutePicker,
    listAgents: client.listAgents,
    listRoutes: client.listRoutes,
    listProviders: client.listProviders,
    availableModels: client.availableModels,
  }
}

describe('AgentRoutePicker', () => {
  it("shows 'Auto' when agent is auto and no route is selected", async () => {
    const { AgentRoutePicker, listAgents, listRoutes } = await freshPicker()
    vi.mocked(listAgents).mockResolvedValue(agents)
    vi.mocked(listRoutes).mockResolvedValue(routes)
    render(
      <AgentRoutePicker
        agent="auto"
        onAgent={vi.fn()}
        route=""
        onRoute={vi.fn()}
        modelHint=""
        onModelHint={vi.fn()}
      />,
    )

    expect(await screen.findByRole('button', { name: 'Agent, route and model' })).toHaveTextContent(
      'Auto',
    )
  })

  it("shows a combined 'agent · route · model' label when all are set", async () => {
    const { AgentRoutePicker, listAgents, listRoutes } = await freshPicker()
    vi.mocked(listAgents).mockResolvedValue(agents)
    vi.mocked(listRoutes).mockResolvedValue(routes)
    render(
      <AgentRoutePicker
        agent="1"
        onAgent={vi.fn()}
        route="local"
        onRoute={vi.fn()}
        modelHint="Anthropic/opus"
        onModelHint={vi.fn()}
      />,
    )

    const trigger = await screen.findByRole('button', { name: 'Agent, route and model' })
    await waitFor(() => expect(trigger).toHaveTextContent('general · local · opus'))
  })

  it('falls back to the agent label when the selected route is disabled', async () => {
    const { AgentRoutePicker, listAgents, listRoutes } = await freshPicker()
    vi.mocked(listAgents).mockResolvedValue(agents)
    vi.mocked(listRoutes).mockResolvedValue([
      ...routes,
      { name: 'disabled-route', strategy: 'ordered', enabled: false, chain: [] },
    ])
    render(
      <AgentRoutePicker
        agent="1"
        onAgent={vi.fn()}
        route="disabled-route"
        onRoute={vi.fn()}
      />,
    )

    const trigger = await screen.findByRole('button', { name: 'Agent, route and model' })
    await waitFor(() => expect(trigger).toHaveTextContent('general'))
    expect(trigger).not.toHaveTextContent('disabled-route')
  })

  it('shows Agent, Route and Model rows in the root panel', async () => {
    const { AgentRoutePicker, listAgents, listRoutes } = await freshPicker()
    vi.mocked(listAgents).mockResolvedValue(agents)
    vi.mocked(listRoutes).mockResolvedValue(routes)
    render(
      <AgentRoutePicker
        agent="auto"
        onAgent={vi.fn()}
        route=""
        onRoute={vi.fn()}
        modelHint=""
        onModelHint={vi.fn()}
      />,
    )

    await screen.findByRole('button', { name: 'Agent, route and model' })
    openMenu('Agent, route and model')
    expect(await screen.findByText('Select')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Agent' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Route' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Model' })).toBeTruthy()
  })

  it('omits the Route row when onRoute is not passed', async () => {
    const { AgentRoutePicker, listAgents, listRoutes } = await freshPicker()
    vi.mocked(listAgents).mockResolvedValue(agents)
    vi.mocked(listRoutes).mockResolvedValue(routes)
    render(<AgentRoutePicker agent="auto" onAgent={vi.fn()} />)

    await screen.findByRole('button', { name: 'Agent, route and model' })
    openMenu('Agent, route and model')
    expect(await screen.findByRole('button', { name: 'Agent' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Route' })).toBeNull()
  })

  it('omits the Route row when listRoutes rejects, Agent panel still works', async () => {
    const { AgentRoutePicker, listAgents, listRoutes } = await freshPicker()
    vi.mocked(listAgents).mockResolvedValue(agents)
    vi.mocked(listRoutes).mockRejectedValue(new Error('forbidden'))
    render(<AgentRoutePicker agent="auto" onAgent={vi.fn()} route="" onRoute={vi.fn()} />)

    await screen.findByRole('button', { name: 'Agent, route and model' })
    await waitFor(() => expect(listRoutes).toHaveBeenCalled())
    openMenu('Agent, route and model')
    expect(await screen.findByRole('button', { name: 'Agent' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Route' })).toBeNull()
    openPanel('Agent')
    expect(await screen.findByText('researcher')).toBeTruthy()
  })

  it('shows the chain models on a route item, or a placeholder when empty', async () => {
    const { AgentRoutePicker, listAgents, listRoutes } = await freshPicker()
    vi.mocked(listAgents).mockResolvedValue(agents)
    vi.mocked(listRoutes).mockResolvedValue(routes)
    render(<AgentRoutePicker agent="auto" onAgent={vi.fn()} route="" onRoute={vi.fn()} />)

    await screen.findByRole('button', { name: 'Agent, route and model' })
    openMenu('Agent, route and model')
    openPanel('Route')
    expect(await screen.findByText('glm-5.2 → nova-pro')).toBeTruthy()
    expect(screen.getByText('No models configured')).toBeTruthy()
  })

  it('clicking a route item calls onRoute', async () => {
    const { AgentRoutePicker, listAgents, listRoutes } = await freshPicker()
    vi.mocked(listAgents).mockResolvedValue(agents)
    vi.mocked(listRoutes).mockResolvedValue(routes)
    const onRoute = vi.fn()
    render(<AgentRoutePicker agent="auto" onAgent={vi.fn()} route="" onRoute={onRoute} />)

    await screen.findByRole('button', { name: 'Agent, route and model' })
    openMenu('Agent, route and model')
    openPanel('Route')
    fireEvent.click(await screen.findByText('local'))
    expect(onRoute).toHaveBeenCalledWith('local')
  })

  it('clicking a model item calls onModelHint', async () => {
    const { AgentRoutePicker, listAgents, listRoutes, listProviders, availableModels } =
      await freshPicker()
    vi.mocked(listAgents).mockResolvedValue(agents)
    vi.mocked(listRoutes).mockResolvedValue(routes)
    const onModelHint = vi.fn()
    render(
      <AgentRoutePicker
        agent="auto"
        onAgent={vi.fn()}
        route=""
        onRoute={vi.fn()}
        modelHint=""
        onModelHint={onModelHint}
      />,
    )

    await screen.findByRole('button', { name: 'Agent, route and model' })
    await waitFor(() => expect(listProviders).toHaveBeenCalled())
    await waitFor(() => expect(availableModels).toHaveBeenCalled())
    openMenu('Agent, route and model')
    await waitFor(() => expect(screen.getByRole('button', { name: 'Model' })).toBeInTheDocument())
    openPanel('Model')
    fireEvent.click(await screen.findByText('opus'))
    expect(onModelHint).toHaveBeenCalledWith('Anthropic/opus')
  })

  it('leaves harness (kind cli) provider models out of the model list', async () => {
    const { AgentRoutePicker, listAgents, listRoutes, listProviders, availableModels } =
      await freshPicker()
    vi.mocked(listAgents).mockResolvedValue(agents)
    vi.mocked(listRoutes).mockResolvedValue(routes)
    vi.mocked(listProviders).mockResolvedValue([
      ...providers,
      {
        id: 'p-harness',
        name: 'Claude Code',
        kind: 'cli',
        driver: 'claude-cli',
        base_url: '',
        default_model: 'harness-model',
        credential_ref: '',
        headers: {},
        enabled: true,
      },
    ])
    vi.mocked(availableModels).mockImplementation(async (id: string) =>
      id === 'p-harness' ? [{ id: 'harness-model' }] : models,
    )
    render(
      <AgentRoutePicker
        agent="auto"
        onAgent={vi.fn()}
        route=""
        onRoute={vi.fn()}
        modelHint=""
        onModelHint={vi.fn()}
      />,
    )

    await screen.findByRole('button', { name: 'Agent, route and model' })
    openMenu('Agent, route and model')
    openPanel('Model')
    expect(await screen.findByText('opus')).toBeInTheDocument()
    expect(screen.queryByText('harness-model')).not.toBeInTheDocument()
    expect(availableModels).not.toHaveBeenCalledWith('p-harness')
  })

  it('clicking an agent item calls onAgent', async () => {
    const { AgentRoutePicker, listAgents, listRoutes } = await freshPicker()
    vi.mocked(listAgents).mockResolvedValue(agents)
    vi.mocked(listRoutes).mockResolvedValue(routes)
    const onAgent = vi.fn()
    render(<AgentRoutePicker agent="auto" onAgent={onAgent} route="" onRoute={vi.fn()} />)

    await screen.findByRole('button', { name: 'Agent, route and model' })
    openMenu('Agent, route and model')
    openPanel('Agent')
    fireEvent.click(await screen.findByText('researcher'))
    expect(onAgent).toHaveBeenCalledWith('2')
  })
})
