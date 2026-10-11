import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdminAgent, AdminConnector, ConnectorProbe } from '../../api/types'
import { ConnectorMCPTools } from './ConnectorMCPTools'

vi.mock('../../api/client', () => ({
  listAgents: vi.fn(),
  patchAgent: vi.fn(),
  patchConnector: vi.fn(),
  probeConnector: vi.fn(),
}))
vi.mock('sonner', () => ({ toast: { error: vi.fn(), success: vi.fn() } }))

import { listAgents, patchAgent, patchConnector, probeConnector } from '../../api/client'
import { toast } from 'sonner'

const agents: AdminAgent[] = [
  { id: 'a1', name: 'general', description: '', prompt_overlay: '', route: '', skills: [], tools: ['shell', 'search', 'notion_shell'], memory: true, is_default: true, enabled: true },
  { id: 'a2', name: 'research', description: '', prompt_overlay: '', route: '', skills: [], tools: ['search'], memory: true, is_default: false, enabled: true },
  { id: 'a3', name: 'retired', description: '', prompt_overlay: '', route: '', skills: [], tools: ['search'], memory: true, is_default: false, enabled: false },
]

const connector: AdminConnector = {
  id: 'm1',
  name: 'notion',
  kind: 'mcp',
  config: {
    endpoint: 'https://mcp.notion.com/mcp',
    last_probe: {
      at: '2026-10-10T10:00:00Z',
      tool_count: 3,
      hash: 'h1',
      tools: [
        { name: 'search', final_name: 'search', read_only_hint: true },
        { name: 'shell', final_name: 'notion_shell', read_only_hint: null },
        { name: 'old_tool', final_name: 'old_tool', read_only_hint: false },
      ],
    },
  },
  credential_ref: 'NOTION_MCP_TOKEN',
  enabled: true,
  sensitive: false,
}

const reprobed: ConnectorProbe = {
  status: 'ok',
  server: { name: 'Notion', version: '2' },
  tool_count: 3,
  index_threshold: 8,
  hash: 'h2',
  tools: [
    { name: 'search', final_name: 'search', description: '', read_only_hint: true, input_schema: {} },
    { name: 'shell', final_name: 'notion_shell', description: '', read_only_hint: null, input_schema: {} },
    { name: 'create_page', final_name: 'create_page', description: '', read_only_hint: false, input_schema: {} },
  ],
  added: ['create_page'],
  removed: ['old_tool'],
}

const failed = (status: ConnectorProbe['status'], message = ''): ConnectorProbe => ({
  status,
  server: { name: '', version: '' },
  tools: [],
  tool_count: 0,
  index_threshold: 8,
  message,
})

function row(tool: string) {
  return screen.getByText(tool, { selector: 'span' }).closest('tr') as HTMLElement
}

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(listAgents).mockResolvedValue(agents)
  vi.mocked(patchAgent).mockResolvedValue(undefined)
  vi.mocked(patchConnector).mockResolvedValue(undefined)
})

function checked(label: string): string | null {
  return (screen.getByLabelText(label) as HTMLButtonElement).getAttribute('aria-checked')
}

describe('ConnectorMCPTools', () => {
  it('lists the stored tools with each enabled agent allow state', async () => {
    const onProbed = vi.fn(() => Promise.resolve())
    render(<ConnectorMCPTools connector={connector} onProbed={onProbed} />)

    expect(screen.getByText(/Last checked/)).toBeTruthy()
    expect(await screen.findByLabelText('Allow search for general')).toBeTruthy()
    expect((screen.getByLabelText('Allow search for general') as HTMLButtonElement).getAttribute('aria-checked')).toBe('true')
    expect((screen.getByLabelText('Allow search for research') as HTMLButtonElement).getAttribute('aria-checked')).toBe('true')
    // The allowlist carries the final name, so shell is allowed for general.
    expect((screen.getByLabelText('Allow shell for general') as HTMLButtonElement).getAttribute('aria-checked')).toBe('true')
    expect((screen.getByLabelText('Allow shell for research') as HTMLButtonElement).getAttribute('aria-checked')).toBe('false')
    expect(screen.getByText('as notion_shell')).toBeTruthy()
    expect(screen.queryByLabelText('Allow search for retired')).toBeNull()
    expect(within(row('search')).getByText('read-only')).toBeTruthy()
    expect(within(row('old_tool')).getByText('writes')).toBeTruthy()
    expect(within(row('shell')).getByText('not stated')).toBeTruthy()
  })

  it('shows the empty state for a connector without a record', () => {
    render(<ConnectorMCPTools connector={{ ...connector, config: { endpoint: 'https://x' } }} onProbed={() => Promise.resolve()} />)
    expect(screen.getByText('Not checked yet.')).toBeTruthy()
    expect(screen.getByText(/No tools recorded yet/)).toBeTruthy()
  })

  it('re-probes by connector id and marks added and removed tools', async () => {
    vi.mocked(probeConnector).mockResolvedValue(reprobed)
    const onProbed = vi.fn(() => Promise.resolve())
    render(<ConnectorMCPTools connector={connector} onProbed={onProbed} />)
    await screen.findByLabelText('Allow search for general')

    fireEvent.click(screen.getByRole('button', { name: 'Re-probe' }))
    expect(await screen.findByText('1 added, 1 removed since the last check.')).toBeTruthy()
    expect(probeConnector).toHaveBeenCalledWith({ connector_id: 'm1' })
    expect(onProbed).toHaveBeenCalledTimes(1)

    expect(within(row('create_page')).getByText('new')).toBeTruthy()
    expect(within(row('old_tool')).getByText('removed')).toBeTruthy()
    expect((screen.getByLabelText('Allow create_page for general') as HTMLButtonElement).getAttribute('aria-checked')).toBe('false')
    expect((screen.getByLabelText('Allow create_page for research') as HTMLButtonElement).getAttribute('aria-checked')).toBe('false')
    expect((screen.getByLabelText('Allow old_tool for general') as HTMLButtonElement).hasAttribute('disabled')).toBe(true)
  })

  it('reports an unchanged list', async () => {
    vi.mocked(probeConnector).mockResolvedValue({ ...reprobed, added: undefined, removed: undefined })
    render(<ConnectorMCPTools connector={connector} onProbed={() => Promise.resolve()} />)
    fireEvent.click(screen.getByRole('button', { name: 'Re-probe' }))
    expect(await screen.findByText('Tool list unchanged.')).toBeTruthy()
  })

  it('patches only the toggled agent allowlist', async () => {
    render(<ConnectorMCPTools connector={connector} onProbed={() => Promise.resolve()} />)
    fireEvent.click(await screen.findByLabelText('Allow search for research'))
    await waitFor(() => expect(patchAgent).toHaveBeenCalledWith('a2', { tools: [] }))
    expect(patchAgent).toHaveBeenCalledTimes(1)
    await waitFor(() =>
      expect((screen.getByLabelText('Allow search for research') as HTMLButtonElement).getAttribute('aria-checked')).toBe('false'),
    )
    expect((screen.getByLabelText('Allow search for general') as HTMLButtonElement).getAttribute('aria-checked')).toBe('true')

    fireEvent.click(screen.getByLabelText('Allow shell for research'))
    await waitFor(() => expect(patchAgent).toHaveBeenCalledWith('a2', { tools: ['notion_shell'] }))
  })

  it('keeps the allow state and reports when a patch fails', async () => {
    vi.mocked(patchAgent).mockRejectedValue(new Error('boom'))
    render(<ConnectorMCPTools connector={connector} onProbed={() => Promise.resolve()} />)
    fireEvent.click(await screen.findByLabelText('Allow search for research'))
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith('Could not update research', { description: 'boom' }))
    expect((screen.getByLabelText('Allow search for research') as HTMLButtonElement).getAttribute('aria-checked')).toBe('true')
  })

  it('asks to reconnect when the oauth session is gone', async () => {
    vi.mocked(probeConnector).mockResolvedValue(failed('needs_oauth', 'reconnect to re-authorize'))
    const reconnect = vi.fn()
    render(<ConnectorMCPTools connector={connector} onProbed={() => Promise.resolve()} reconnect={reconnect} />)
    fireEvent.click(screen.getByRole('button', { name: 'Re-probe' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Reconnect' }))
    expect(reconnect).toHaveBeenCalledTimes(1)
    // The stored list stays on screen.
    expect(await screen.findByLabelText('Allow search for general')).toBeTruthy()
  })

  it.each([
    ['needs_token', 'The server rejected the stored token. Rotate it in the Connection panel.'],
    ['unreachable', 'Could not reach the server.'],
    ['error', 'The server answered, but the check failed.'],
  ] as const)('%s shows its reason', async (status, text) => {
    vi.mocked(probeConnector).mockResolvedValue(failed(status, 'detail'))
    render(<ConnectorMCPTools connector={connector} onProbed={() => Promise.resolve()} />)
    fireEvent.click(screen.getByRole('button', { name: 'Re-probe' }))
    expect(await screen.findByText(text)).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Reconnect' })).toBeNull()
  })
})

describe('ConnectorMCPTools mission reads', () => {
  const marked: AdminConnector = {
    ...connector,
    config: { ...connector.config, mission_reads: { hash: 'h1', tools: ['search'] } },
  }

  it('shows the marks only while the stored fingerprint matches the review', async () => {
    render(<ConnectorMCPTools connector={marked} onProbed={() => Promise.resolve()} />)
    await screen.findByLabelText('Allow search for general')
    expect(checked('Missions may read search')).toBe('true')
    expect(checked('Missions may read shell')).toBe('false')
    expect(screen.queryByText(/changed since you reviewed/)).toBeNull()
  })

  it('marks a tool with the current fingerprint and keeps other config', async () => {
    const onProbed = vi.fn(() => Promise.resolve())
    render(<ConnectorMCPTools connector={marked} onProbed={onProbed} />)
    fireEvent.click(await screen.findByLabelText('Missions may read shell'))
    await waitFor(() =>
      expect(patchConnector).toHaveBeenCalledWith('m1', {
        config: { ...marked.config, mission_reads: { hash: 'h1', tools: ['search', 'shell'] } },
      }),
    )
    expect(onProbed).toHaveBeenCalledTimes(1)
  })

  it('drops the field when the last mark is removed', async () => {
    render(<ConnectorMCPTools connector={marked} onProbed={() => Promise.resolve()} />)
    fireEvent.click(await screen.findByLabelText('Missions may read search'))
    await waitFor(() => expect(patchConnector).toHaveBeenCalledWith('m1', { config: connector.config }))
  })

  it('disables the marks and asks for a re-probe when the record has no fingerprint', () => {
    const old: AdminConnector = {
      ...marked,
      config: {
        ...marked.config,
        last_probe: { at: '2026-10-10T10:00:00Z', tool_count: 1, tools: [{ name: 'search', read_only_hint: null }] },
      },
    }
    render(<ConnectorMCPTools connector={old} onProbed={() => Promise.resolve()} />)
    expect((screen.getByLabelText('Missions may read search') as HTMLButtonElement).hasAttribute('disabled')).toBe(true)
    expect(screen.getByText(/re-probe to record the server/)).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Confirm marks' })).toBeNull()
  })

  it('lapses the marks after a re-probe with a new fingerprint and confirms them against the new list', async () => {
    vi.mocked(probeConnector).mockResolvedValue(reprobed)
    render(<ConnectorMCPTools connector={marked} onProbed={() => Promise.resolve()} />)
    await screen.findByLabelText('Allow search for general')
    fireEvent.click(screen.getByRole('button', { name: 'Re-probe' }))
    await screen.findByText(/changed since you reviewed/)
    expect(checked('Missions may read search')).toBe('false')

    fireEvent.click(screen.getByRole('button', { name: 'Confirm marks' }))
    await waitFor(() =>
      expect(patchConnector).toHaveBeenCalledWith('m1', {
        config: { ...marked.config, mission_reads: { hash: 'h2', tools: ['search'] } },
      }),
    )
  })

  it('confirms only the tools the server still serves', async () => {
    const stale: AdminConnector = {
      ...marked,
      config: { ...marked.config, mission_reads: { hash: 'h1', tools: ['search', 'old_tool'] } },
    }
    vi.mocked(probeConnector).mockResolvedValue(reprobed)
    render(<ConnectorMCPTools connector={stale} onProbed={() => Promise.resolve()} />)
    fireEvent.click(screen.getByRole('button', { name: 'Re-probe' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Confirm marks' }))
    await waitFor(() =>
      expect(patchConnector).toHaveBeenCalledWith('m1', {
        config: { ...stale.config, mission_reads: { hash: 'h2', tools: ['search'] } },
      }),
    )
  })

  it('reports a failed save and keeps the marks', async () => {
    vi.mocked(patchConnector).mockRejectedValue(new Error('boom'))
    render(<ConnectorMCPTools connector={marked} onProbed={() => Promise.resolve()} />)
    fireEvent.click(await screen.findByLabelText('Missions may read shell'))
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith('Could not save mission reads', { description: 'boom' }))
    expect(checked('Missions may read search')).toBe('true')
  })
})

describe('ConnectorMCPTools broad allowlist entries', () => {
  it('shows a tool allowed by a raw entry as checked and locked', async () => {
    vi.mocked(listAgents).mockResolvedValue([
      { ...agents[1], tools: ['shell'] },
    ])
    const prefixed: AdminConnector = {
      ...connector,
      config: {
        ...connector.config,
        last_probe: { at: '2026-10-10T10:00:00Z', tool_count: 1, hash: 'h1', tools: [{ name: 'shell', final_name: 'notion_shell', read_only_hint: null }] },
      },
    }
    render(<ConnectorMCPTools connector={prefixed} onProbed={() => Promise.resolve()} />)
    const box = (await screen.findByLabelText('Allow shell for research')) as HTMLButtonElement
    expect(box.getAttribute('aria-checked')).toBe('true')
    expect(box.hasAttribute('disabled')).toBe(true)
    expect(screen.getByText('via shell')).toBeTruthy()
    fireEvent.click(box)
    expect(patchAgent).not.toHaveBeenCalled()
  })
})
