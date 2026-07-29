import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdminAgent, AdminRoute } from '../api/types'

vi.mock('../api/client', () => ({
  listAgents: vi.fn(),
  listRoutes: vi.fn(),
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

// Radix opens dropdowns on pointerdown, not click.
function openMenu(name: string) {
  const trigger = screen.getByRole('button', { name })
  fireEvent.pointerDown(trigger, { button: 0, pointerId: 1 })
  fireEvent.click(trigger)
}

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
})

// AgentRoutePicker composes AgentPicker's useAgents and its own
// useRoutes, both module-level caches — each test resets modules and
// re-imports fresh to avoid one test's fetch leaking into the next.
async function freshPicker() {
  vi.resetModules()
  const client = await import('../api/client')
  const { AgentRoutePicker } = await import('./AgentRoutePicker')
  return { AgentRoutePicker, listAgents: client.listAgents, listRoutes: client.listRoutes }
}

describe('AgentRoutePicker', () => {
  it("shows 'Auto' when agent is auto and no route is selected", async () => {
    const { AgentRoutePicker, listAgents, listRoutes } = await freshPicker()
    vi.mocked(listAgents).mockResolvedValue(agents)
    vi.mocked(listRoutes).mockResolvedValue(routes)
    render(<AgentRoutePicker agent="auto" onAgent={vi.fn()} route="" onRoute={vi.fn()} />)

    expect(await screen.findByRole('button', { name: 'Agent and route' })).toHaveTextContent(
      'Auto',
    )
  })

  it("shows a combined 'agent · route' label when both are set", async () => {
    const { AgentRoutePicker, listAgents, listRoutes } = await freshPicker()
    vi.mocked(listAgents).mockResolvedValue(agents)
    vi.mocked(listRoutes).mockResolvedValue(routes)
    render(<AgentRoutePicker agent="general" onAgent={vi.fn()} route="local" onRoute={vi.fn()} />)

    const trigger = await screen.findByRole('button', { name: 'Agent and route' })
    await waitFor(() => expect(trigger).toHaveTextContent('general · local'))
  })

  it('shows both Agent and Route sections when onRoute is given', async () => {
    const { AgentRoutePicker, listAgents, listRoutes } = await freshPicker()
    vi.mocked(listAgents).mockResolvedValue(agents)
    vi.mocked(listRoutes).mockResolvedValue(routes)
    render(<AgentRoutePicker agent="auto" onAgent={vi.fn()} route="" onRoute={vi.fn()} />)

    await screen.findByRole('button', { name: 'Agent and route' })
    openMenu('Agent and route')
    expect(await screen.findByText('Agent')).toBeTruthy()
    expect(await screen.findByText('Route')).toBeTruthy()
    expect(screen.getByText('researcher')).toBeTruthy()
    expect(screen.getByText('default')).toBeTruthy()
    expect(screen.getByText('local')).toBeTruthy()
  })

  it('omits the Route section when onRoute is not passed', async () => {
    const { AgentRoutePicker, listAgents, listRoutes } = await freshPicker()
    vi.mocked(listAgents).mockResolvedValue(agents)
    vi.mocked(listRoutes).mockResolvedValue(routes)
    render(<AgentRoutePicker agent="auto" onAgent={vi.fn()} />)

    await screen.findByRole('button', { name: 'Agent and route' })
    openMenu('Agent and route')
    expect(await screen.findByText('Agent')).toBeTruthy()
    expect(screen.queryByText('Route')).toBeNull()
  })

  it('omits the Route section when listRoutes rejects, Agent section still works', async () => {
    const { AgentRoutePicker, listAgents, listRoutes } = await freshPicker()
    vi.mocked(listAgents).mockResolvedValue(agents)
    vi.mocked(listRoutes).mockRejectedValue(new Error('forbidden'))
    render(<AgentRoutePicker agent="auto" onAgent={vi.fn()} route="" onRoute={vi.fn()} />)

    await screen.findByRole('button', { name: 'Agent and route' })
    await waitFor(() => expect(listRoutes).toHaveBeenCalled())
    openMenu('Agent and route')
    expect(await screen.findByText('Agent')).toBeTruthy()
    expect(screen.getByText('researcher')).toBeTruthy()
    expect(screen.queryByText('Route')).toBeNull()
  })

  it('shows the chain models on a route item, or a placeholder when empty', async () => {
    const { AgentRoutePicker, listAgents, listRoutes } = await freshPicker()
    vi.mocked(listAgents).mockResolvedValue(agents)
    vi.mocked(listRoutes).mockResolvedValue(routes)
    render(<AgentRoutePicker agent="auto" onAgent={vi.fn()} route="" onRoute={vi.fn()} />)

    await screen.findByRole('button', { name: 'Agent and route' })
    openMenu('Agent and route')
    expect(await screen.findByText('glm-5.2 → nova-pro')).toBeTruthy()
    expect(screen.getByText('No models configured')).toBeTruthy()
  })

  it('clicking a route item calls onRoute', async () => {
    const { AgentRoutePicker, listAgents, listRoutes } = await freshPicker()
    vi.mocked(listAgents).mockResolvedValue(agents)
    vi.mocked(listRoutes).mockResolvedValue(routes)
    const onRoute = vi.fn()
    render(<AgentRoutePicker agent="auto" onAgent={vi.fn()} route="" onRoute={onRoute} />)

    await screen.findByRole('button', { name: 'Agent and route' })
    openMenu('Agent and route')
    fireEvent.click(await screen.findByText('local'))
    expect(onRoute).toHaveBeenCalledWith('local')
  })

  it('clicking an agent item calls onAgent', async () => {
    const { AgentRoutePicker, listAgents, listRoutes } = await freshPicker()
    vi.mocked(listAgents).mockResolvedValue(agents)
    vi.mocked(listRoutes).mockResolvedValue(routes)
    const onAgent = vi.fn()
    render(<AgentRoutePicker agent="auto" onAgent={onAgent} route="" onRoute={vi.fn()} />)

    await screen.findByRole('button', { name: 'Agent and route' })
    openMenu('Agent and route')
    fireEvent.click(await screen.findByText('researcher'))
    expect(onAgent).toHaveBeenCalledWith('researcher')
  })
})
