import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdminConnector } from '../../api/types'
import { PushBranchDialog } from './PushBranchDialog'

vi.mock('../../api/client', () => ({
  pushMission: vi.fn(),
  secretStatus: vi.fn(),
  listConnectors: vi.fn(),
}))

import { listConnectors, secretStatus } from '../../api/client'

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
  localStorage.clear()
  vi.mocked(listConnectors).mockResolvedValue([])
  vi.mocked(secretStatus).mockResolvedValue({ configured: false, backend: 'db' })
})

const githubConnector: AdminConnector = {
  id: 'c1',
  name: 'GitHub',
  kind: 'mcp',
  config: { endpoint: 'https://api.githubcopilot.com/mcp/' },
  credential_ref: 'github_pat',
  enabled: true,
}

describe('PushBranchDialog', () => {
  it('prefills the GitHub connector credential_ref when nothing is remembered', async () => {
    vi.mocked(listConnectors).mockResolvedValue([githubConnector])
    render(<PushBranchDialog missionId="m1" open onOpenChange={() => {}} onPushed={() => {}} />)

    expect(await screen.findByDisplayValue('github_pat')).toBeTruthy()
  })

  it('leaves the field empty when no GitHub connector is configured', async () => {
    vi.mocked(listConnectors).mockResolvedValue([])
    render(<PushBranchDialog missionId="m1" open onOpenChange={() => {}} onPushed={() => {}} />)

    await screen.findByLabelText('Credential reference')
    expect(screen.queryByDisplayValue('github_pat')).toBeNull()
  })

  it('prefers a remembered ref over the GitHub connector default', async () => {
    localStorage.setItem('timothy.push_ref', 'my_saved_ref')
    vi.mocked(listConnectors).mockResolvedValue([githubConnector])
    render(<PushBranchDialog missionId="m1" open onOpenChange={() => {}} onPushed={() => {}} />)

    expect(await screen.findByDisplayValue('my_saved_ref')).toBeTruthy()
    expect(screen.queryByDisplayValue('github_pat')).toBeNull()
  })

  it('ignores a non-matching mcp connector', async () => {
    const other: AdminConnector = {
      ...githubConnector,
      config: { endpoint: 'https://example.com/mcp/' },
      credential_ref: 'other_ref',
    }
    vi.mocked(listConnectors).mockResolvedValue([other])
    render(<PushBranchDialog missionId="m1" open onOpenChange={() => {}} onPushed={() => {}} />)

    await screen.findByLabelText('Credential reference')
    expect(screen.queryByDisplayValue('other_ref')).toBeNull()
  })

  it('lets the user override the prefilled value before submitting', async () => {
    vi.mocked(listConnectors).mockResolvedValue([githubConnector])
    render(<PushBranchDialog missionId="m1" open onOpenChange={() => {}} onPushed={() => {}} />)

    const input = await screen.findByDisplayValue('github_pat')
    fireEvent.change(input, { target: { value: 'custom_ref' } })
    expect(screen.getByDisplayValue('custom_ref')).toBeTruthy()
  })
})
