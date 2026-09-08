import axe from 'axe-core'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdminProvider, AdminRoute } from '../../api/types'
import { TooltipProvider } from '../ui/tooltip'
import { RoutesList } from './RoutesList'

vi.mock('../../api/client', () => ({
  listRoutes: vi.fn(),
  listProviders: vi.fn(),
  patchRoute: vi.fn(),
  createRoute: vi.fn(),
  deleteRoute: vi.fn(),
  setRouteRole: vi.fn(),
}))
vi.mock('sonner', () => ({ toast: { error: vi.fn(), success: vi.fn() } }))

import {
  createRoute,
  deleteRoute,
  listProviders,
  listRoutes,
  patchRoute,
  setRouteRole,
} from '../../api/client'
import { toast } from 'sonner'

const providers: AdminProvider[] = [
  {
    id: 'p1', name: 'anthropic', kind: 'api', driver: 'anthropic', base_url: '',
    default_model: 'sonnet', credential_ref: 'A_KEY', headers: {}, enabled: true,
  },
]

const base: AdminRoute = {
  name: 'default',
  strategy: 'ordered',
  enabled: true,
  chain: [{ provider_id: 'p1', model: 'sonnet' }],
}

afterEach(cleanup)
beforeEach(() => {
  Element.prototype.scrollIntoView = vi.fn()
  vi.clearAllMocks()
  vi.mocked(listProviders).mockResolvedValue(providers)
})

function renderList() {
  return render(
    <TooltipProvider>
      <MemoryRouter>
        <RoutesList />
      </MemoryRouter>
    </TooltipProvider>,
  )
}

describe('RoutesList serving states', () => {
  it('shows the router-resolved serving entry', async () => {
    vi.mocked(listRoutes).mockResolvedValue([
      {
        ...base,
        resolved: [{ provider_id: 'p1', provider_name: 'anthropic', model: 'sonnet', usable: true }],
        serving: { provider_id: 'p1', model: 'sonnet' },
      },
    ])
    renderList()
    expect(await screen.findAllByText('sonnet')).toHaveLength(2)
    expect(screen.getByText(/serving/)).toBeInTheDocument()
  })

  it('warns when nothing is usable', async () => {
    vi.mocked(listRoutes).mockResolvedValue([
      {
        ...base,
        resolved: [{ provider_id: 'p1', model: 'sonnet', usable: false, skip_reason: 'disabled' }],
      },
    ])
    renderList()
    expect(await screen.findByText('no usable provider')).toBeInTheDocument()
  })

  it('marks a disabled route', async () => {
    vi.mocked(listRoutes).mockResolvedValue([{ ...base, enabled: false }])
    renderList()
    expect(await screen.findByText('disabled')).toBeInTheDocument()
  })

  it('shows a loading state before the snapshot has stats', async () => {
    vi.mocked(listRoutes).mockResolvedValue([base])
    renderList()
    expect(await screen.findByText('stats loading…')).toBeInTheDocument()
  })
})

describe('RoutesList actions', () => {
  it('creates a route with the picked name and capability', async () => {
    vi.mocked(listRoutes).mockResolvedValue([base])
    vi.mocked(createRoute).mockResolvedValue('r-new')
    renderList()

    await screen.findByText('default')
    fireEvent.change(screen.getByLabelText('New route name'), { target: { value: 'my-route' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add route' }))

    await waitFor(() => expect(createRoute).toHaveBeenCalledWith('my-route', 'chat'))
  })

  it('toggles a route enabled from the card', async () => {
    vi.mocked(listRoutes).mockResolvedValue([base])
    vi.mocked(patchRoute).mockResolvedValue(undefined)
    renderList()

    const toggle = await screen.findByRole('switch', { name: 'default route enabled' })
    fireEvent.click(toggle)

    await waitFor(() => expect(patchRoute).toHaveBeenCalledWith('default', { enabled: false }))
  })

  it('deletes an unassigned route from the card', async () => {
    vi.mocked(listRoutes).mockResolvedValue([base])
    vi.mocked(deleteRoute).mockResolvedValue(undefined)
    renderList()

    await screen.findByText('default')
    fireEvent.click(screen.getByRole('button', { name: 'Delete default route' }))

    await waitFor(() => expect(deleteRoute).toHaveBeenCalledWith('default'))
  })

  it('disables delete for a route bound to a system role', async () => {
    vi.mocked(listRoutes).mockResolvedValue([{ ...base, role: 'default' }])
    renderList()

    const deleteButton = await screen.findByRole('button', {
      name: 'Serves the default role, reassign that role first',
    })
    expect(deleteButton).toBeDisabled()
  })

  it('assigns a system role to a route via the role picker', async () => {
    vi.mocked(listRoutes).mockResolvedValue([base])
    vi.mocked(setRouteRole).mockResolvedValue(undefined)
    renderList()

    await screen.findByText('default')
    fireEvent.click(screen.getByRole('combobox', { name: 'Chat (default) route' }))
    fireEvent.click(await screen.findByRole('option', { name: 'default' }))

    await waitFor(() => expect(setRouteRole).toHaveBeenCalledWith('default', 'default'))
  })

  it('toasts when toggling a route fails', async () => {
    vi.mocked(listRoutes).mockResolvedValue([base])
    vi.mocked(patchRoute).mockRejectedValue(new Error('locked'))
    renderList()

    fireEvent.click(await screen.findByRole('switch', { name: 'default route enabled' }))
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Could not update route', { description: 'locked' }),
    )
  })

  it('toasts when creating a route fails', async () => {
    vi.mocked(listRoutes).mockResolvedValue([base])
    vi.mocked(createRoute).mockRejectedValue(new Error('name taken'))
    renderList()

    await screen.findByText('default')
    fireEvent.change(screen.getByLabelText('New route name'), { target: { value: 'default' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add route' }))

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Could not create route', { description: 'name taken' }),
    )
  })

  it('toasts when deleting a route fails', async () => {
    vi.mocked(listRoutes).mockResolvedValue([base])
    vi.mocked(deleteRoute).mockRejectedValue(new Error('in use'))
    renderList()

    await screen.findByText('default')
    fireEvent.click(screen.getByRole('button', { name: 'Delete default route' }))

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Could not delete route', { description: 'in use' }),
    )
  })

  it('toasts when assigning a role fails', async () => {
    vi.mocked(listRoutes).mockResolvedValue([base])
    vi.mocked(setRouteRole).mockRejectedValue(new Error('conflict'))
    renderList()

    await screen.findByText('default')
    fireEvent.click(screen.getByRole('combobox', { name: 'Chat (default) route' }))
    fireEvent.click(await screen.findByRole('option', { name: 'default' }))

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Could not assign role', { description: 'conflict' }),
    )
  })

  it('toasts when loading routes fails', async () => {
    vi.mocked(listRoutes).mockRejectedValue(new Error('network down'))
    renderList()

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Could not load routes', { description: 'network down' }),
    )
  })

  it('shows "no providers in chain" for an empty chain', async () => {
    vi.mocked(listRoutes).mockResolvedValue([{ ...base, chain: [] }])
    renderList()

    expect(await screen.findByText('no providers in chain')).toBeTruthy()
  })

  it('falls back to the resolved model when the chain entry has an empty model', async () => {
    vi.mocked(listRoutes).mockResolvedValue([
      {
        ...base,
        chain: [{ provider_id: 'p1', model: '' }],
        resolved: [{ provider_id: 'p1', provider_name: 'anthropic', model: 'sonnet', usable: true }],
      },
    ])
    renderList()

    expect(await screen.findByText('sonnet')).toBeTruthy()
  })
})

describe('RoutesList accessibility', () => {
  it('has no axe violations', async () => {
    vi.mocked(listRoutes).mockResolvedValue([
      {
        ...base,
        role: 'default',
        resolved: [{ provider_id: 'p1', provider_name: 'anthropic', model: 'sonnet', usable: true }],
        serving: { provider_id: 'p1', model: 'sonnet' },
      },
    ])
    const { container } = renderList()
    await screen.findByText(/serving/)
    const results = await axe.run(container, { rules: { 'color-contrast': { enabled: false } } })
    expect(results.violations).toEqual([])
  })
})
