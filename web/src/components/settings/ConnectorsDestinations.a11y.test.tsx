import axe from 'axe-core'
import { render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdminConnector, Destination } from '../../api/types'
import { ConnectorsTab } from './ConnectorsTab'
import { DestinationsTab } from './DestinationsTab'

vi.mock('../../api/client', () => ({
  connectorOAuthStart: vi.fn(),
  createConnector: vi.fn(),
  createDestination: vi.fn(),
  deleteConnector: vi.fn(),
  deleteDestination: vi.fn(),
  listConnectors: vi.fn(),
  listDestinations: vi.fn(),
  listSecretBackends: vi.fn(),
  listSecretRefs: vi.fn(),
  patchConnector: vi.fn(),
  patchDestination: vi.fn(),
  setSecret: vi.fn(),
  testConnector: vi.fn(),
  testDestination: vi.fn(),
}))

import {
  listConnectors,
  listDestinations,
  listSecretBackends,
  listSecretRefs,
} from '../../api/client'

const githubConnector: AdminConnector = {
  id: 'gh1',
  name: 'personal-gh',
  kind: 'github',
  config: {},
  credential_ref: 'PERSONAL_GH_GITHUB_PAT',
  enabled: true,
  sensitive: false,
}

const webhookDestination: Destination = {
  id: 'd1',
  name: 'ops-hook',
  kind: 'webhook',
  config: { url: 'https://example.com/hook', format: 'json' },
  credential_ref: '',
  enabled: true,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
}

beforeEach(() => {
  vi.clearAllMocks()
  Element.prototype.scrollIntoView = vi.fn()
  vi.mocked(listConnectors).mockResolvedValue([githubConnector])
  vi.mocked(listDestinations).mockResolvedValue([webhookDestination])
  vi.mocked(listSecretBackends).mockResolvedValue([{ backend: 'db', configured: true, default: true }])
  vi.mocked(listSecretRefs).mockResolvedValue([])
})

const axeOptions = { rules: { 'color-contrast': { enabled: false }, region: { enabled: false } } }

describe('Connectors and destinations accessibility', () => {
  it('has no axe violations on the connectors list', async () => {
    const { container } = render(
      <MemoryRouter initialEntries={['/settings/connectors']}>
        <Routes>
          <Route path="/settings/connectors/*" element={<ConnectorsTab />} />
        </Routes>
      </MemoryRouter>,
    )
    await screen.findByText('Your connectors · 1')
    expect((await axe.run(container, axeOptions)).violations).toEqual([])
  })

  it('has no axe violations on the destinations list', async () => {
    const { container } = render(
      <MemoryRouter initialEntries={['/settings/destinations']}>
        <Routes>
          <Route path="/settings/destinations/*" element={<DestinationsTab />} />
        </Routes>
      </MemoryRouter>,
    )
    await screen.findByText('Your destinations · 1')
    expect((await axe.run(container, axeOptions)).violations).toEqual([])
  })

  it('has no axe violations on ConnectorAdd', async () => {
    const { container } = render(
      <MemoryRouter initialEntries={['/settings/connectors/new/imap']}>
        <Routes>
          <Route path="/settings/connectors/*" element={<ConnectorsTab />} />
        </Routes>
      </MemoryRouter>,
    )
    await screen.findByLabelText('IMAP host')
    expect((await axe.run(container, axeOptions)).violations).toEqual([])
  })

  it('has no axe violations on DestinationEdit', async () => {
    const { container } = render(
      <MemoryRouter initialEntries={['/settings/destinations/d1']}>
        <Routes>
          <Route path="/settings/destinations/*" element={<DestinationsTab />} />
        </Routes>
      </MemoryRouter>,
    )
    await screen.findByDisplayValue('https://example.com/hook')
    expect((await axe.run(container, axeOptions)).violations).toEqual([])
  })
})
