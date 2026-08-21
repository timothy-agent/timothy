import { cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdminProvider, AdminRoute } from '../../api/types'
import { RoutesList } from './RoutesList'

vi.mock('../../api/client', () => ({
  listRoutes: vi.fn(),
  listProviders: vi.fn(),
  patchRoute: vi.fn(),
}))

import { listProviders, listRoutes } from '../../api/client'

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
  vi.clearAllMocks()
  vi.mocked(listProviders).mockResolvedValue(providers)
})

function renderList() {
  return render(
    <MemoryRouter>
      <RoutesList />
    </MemoryRouter>,
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
