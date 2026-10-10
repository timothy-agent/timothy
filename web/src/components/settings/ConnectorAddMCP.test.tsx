import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdminAgent, ConnectorProbe } from '../../api/types'
import { TooltipProvider } from '../ui/tooltip'
import { ConnectorAddMCP } from './ConnectorAddMCP'
import { oauthNotice, stdioIssueURL } from './mcpAddFlow'

vi.mock('../../api/client', () => ({
  createConnector: vi.fn(),
  listAgents: vi.fn(),
  patchAgent: vi.fn(),
  probeConnector: vi.fn(),
  setSecret: vi.fn(),
}))
vi.mock('sonner', () => ({ toast: { error: vi.fn(), success: vi.fn() } }))
vi.mock('../../onboarding/context', async () => {
  const { onboardingState } = await import('../../onboarding/testing')
  return { useOnboarding: () => onboardingState() }
})

import { createConnector, listAgents, patchAgent, probeConnector, setSecret } from '../../api/client'

const agents: AdminAgent[] = [
  { id: 'a1', name: 'general', description: '', prompt_overlay: '', route: '', skills: [], tools: ['shell'], memory: true, is_default: true, enabled: true },
  { id: 'a2', name: 'research', description: '', prompt_overlay: '', route: '', skills: [], tools: [], memory: true, is_default: false, enabled: true },
]

const okProbe: ConnectorProbe = {
  status: 'ok',
  server: { name: 'Notion', version: '1.0' },
  tool_count: 3,
  index_threshold: 8,
  tools: [
    { name: 'search', final_name: 'search', description: 'Search pages', read_only_hint: true, input_schema: { type: 'object', required: ['query'], properties: { query: { type: 'string' } } } },
    { name: 'create_page', final_name: 'create_page', description: 'Create a page', read_only_hint: false, input_schema: { type: 'object' } },
    { name: 'shell', final_name: 'notion_shell', description: 'Clashes with a builtin', read_only_hint: null, input_schema: { type: 'object' } },
  ],
}

const failedProbe = (status: ConnectorProbe['status'], message = ''): ConnectorProbe => ({
  status,
  server: { name: '', version: '' },
  tools: [],
  tool_count: 0,
  index_threshold: 8,
  message,
})

const config = JSON.stringify({
  mcpServers: {
    notion: { url: 'https://mcp.notion.com/mcp' },
    local: { command: 'npx', args: ['some-server'] },
  },
})

function renderPage() {
  return render(
    <TooltipProvider>
      <MemoryRouter initialEntries={['/settings/connectors/new/custom-mcp']}>
        <Routes>
          <Route path="/settings/connectors/new/custom-mcp" element={<ConnectorAddMCP />} />
          <Route path="/settings/connectors" element={<p>connectors list</p>} />
        </Routes>
      </MemoryRouter>
    </TooltipProvider>,
  )
}

function paste(text: string) {
  fireEvent.change(screen.getByLabelText('URL or JSON config'), { target: { value: text } })
}

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
  Element.prototype.scrollIntoView = vi.fn()
  vi.mocked(listAgents).mockResolvedValue(agents)
  vi.mocked(createConnector).mockResolvedValue('conn-1')
  vi.mocked(patchAgent).mockResolvedValue()
  vi.mocked(setSecret).mockResolvedValue()
})

describe('ConnectorAddMCP input step', () => {
  it('prefills the HTTP entry and lists the stdio entry as unsupported', () => {
    renderPage()
    paste(config)
    expect(screen.getByLabelText('Name')).toHaveValue('notion')
    expect(screen.getByText('https://mcp.notion.com/mcp')).toBeInTheDocument()
    expect(screen.getByText(/Needs a local runtime, not supported yet/)).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Track stdio support' })).toHaveAttribute('href', stdioIssueURL)
  })

  it('reports input that is neither a URL nor JSON', () => {
    renderPage()
    paste('not a server')
    expect(screen.getByText('Paste an http(s) URL or a JSON config.')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Check server' })).not.toBeInTheDocument()
  })
})

describe('ConnectorAddMCP probe outcomes', () => {
  it('shows the tool table before anything is saved', async () => {
    vi.mocked(probeConnector).mockResolvedValue(okProbe)
    renderPage()
    paste(config)
    fireEvent.click(screen.getByRole('button', { name: 'Check server' }))

    expect(await screen.findByText('Search pages')).toBeInTheDocument()
    expect(probeConnector).toHaveBeenCalledWith({ name: 'notion', endpoint: 'https://mcp.notion.com/mcp' })
    expect(screen.getByText('as notion_shell')).toBeInTheDocument()
    expect(screen.getByText(/3 tools, within the MCP tool index threshold of 8/)).toBeInTheDocument()
    expect(screen.getByText('read-only')).toBeInTheDocument()
    expect(screen.getByText('writes')).toBeInTheDocument()
    expect(screen.getByText('not stated')).toBeInTheDocument()
    expect(createConnector).not.toHaveBeenCalled()
    expect(setSecret).not.toHaveBeenCalled()
  })

  it('asks for a token on needs_token and probes again with it', async () => {
    vi.mocked(probeConnector).mockResolvedValueOnce(failedProbe('needs_token')).mockResolvedValueOnce(okProbe)
    renderPage()
    paste('https://mcp.example.com/mcp')
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'example' } })
    expect(screen.queryByLabelText('Bearer token')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Check server' }))

    fireEvent.change(await screen.findByLabelText('Bearer token'), { target: { value: 'sk-1' } })
    fireEvent.click(screen.getByRole('button', { name: 'Check server' }))
    await screen.findByText('Search pages')
    expect(probeConnector).toHaveBeenLastCalledWith({ name: 'example', endpoint: 'https://mcp.example.com/mcp', token: 'sk-1' })

    fireEvent.click(screen.getByRole('button', { name: 'Add connector' }))
    await waitFor(() => expect(createConnector).toHaveBeenCalled())
    expect(setSecret).toHaveBeenCalledWith('EXAMPLE_MCP_TOKEN', 'sk-1')
    expect(createConnector).toHaveBeenCalledWith({
      name: 'example',
      kind: 'mcp',
      config: { endpoint: 'https://mcp.example.com/mcp' },
      credential_ref: 'EXAMPLE_MCP_TOKEN',
      enabled: true,
    })
  })

  it('explains OAuth and keeps Add disabled', async () => {
    vi.mocked(probeConnector).mockResolvedValue(failedProbe('needs_oauth'))
    renderPage()
    paste(config)
    fireEvent.click(screen.getByRole('button', { name: 'Check server' }))
    expect(await screen.findByText(oauthNotice)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Add connector' })).toBeDisabled()
  })

  it.each([
    ['unreachable', 'Could not reach the server.'],
    ['error', 'The server answered, but the check failed.'],
  ] as const)('%s shows its reason', async (status, text) => {
    vi.mocked(probeConnector).mockResolvedValue(failedProbe(status, 'dial tcp: connection refused'))
    renderPage()
    paste(config)
    fireEvent.click(screen.getByRole('button', { name: 'Check server' }))
    expect(await screen.findByText(text)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Add connector' })).toBeDisabled()
  })

  it('invalidates the probe when the name changes', async () => {
    vi.mocked(probeConnector).mockResolvedValue(okProbe)
    renderPage()
    paste(config)
    fireEvent.click(screen.getByRole('button', { name: 'Check server' }))
    await screen.findByText('Search pages')
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'notion-2' } })
    expect(screen.queryByText('Search pages')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Add connector' })).toBeDisabled()
  })
})

describe('ConnectorAddMCP save', () => {
  it('creates the connector and adds only the checked tools to the picked agent', async () => {
    vi.mocked(probeConnector).mockResolvedValue(okProbe)
    renderPage()
    paste(config)
    fireEvent.click(screen.getByRole('button', { name: 'Check server' }))
    await screen.findByText('Search pages')

    fireEvent.click(screen.getByRole('checkbox', { name: 'Allow create_page' }))
    fireEvent.click(screen.getByRole('checkbox', { name: 'Allow search' }))
    fireEvent.click(screen.getByRole('checkbox', { name: 'Give tools to general' }))
    fireEvent.click(screen.getByRole('button', { name: 'Add connector' }))

    await screen.findByText('connectors list')
    expect(setSecret).not.toHaveBeenCalled()
    expect(createConnector).toHaveBeenCalledWith({
      name: 'notion',
      kind: 'mcp',
      config: { endpoint: 'https://mcp.notion.com/mcp' },
      credential_ref: '',
      enabled: true,
    })
    expect(patchAgent).toHaveBeenCalledTimes(1)
    expect(patchAgent).toHaveBeenCalledWith('a1', { tools: ['shell', 'notion_shell'] })
  })

  it('keeps config headers on the connector', async () => {
    vi.mocked(probeConnector).mockResolvedValue(okProbe)
    renderPage()
    paste(JSON.stringify({ mcpServers: { team: { url: 'https://t.example.com/mcp', headers: { 'X-Team': 'core' } } } }))
    fireEvent.click(screen.getByRole('button', { name: 'Check server' }))
    await screen.findByText('Search pages')
    expect(probeConnector).toHaveBeenCalledWith({ name: 'team', endpoint: 'https://t.example.com/mcp', headers: { 'X-Team': 'core' } })

    fireEvent.click(screen.getByRole('button', { name: 'Add connector' }))
    await screen.findByText('connectors list')
    expect(createConnector).toHaveBeenCalledWith(
      expect.objectContaining({ config: { endpoint: 'https://t.example.com/mcp', headers: { 'X-Team': 'core' } } }),
    )
    expect(patchAgent).not.toHaveBeenCalled()
  })
})

describe('ConnectorAddMCP Try it', () => {
  it('validates arguments and shows the request without running the tool', async () => {
    vi.mocked(probeConnector).mockResolvedValue(okProbe)
    renderPage()
    paste(config)
    fireEvent.click(screen.getByRole('button', { name: 'Check server' }))
    await screen.findByText('Search pages')

    fireEvent.click(screen.getAllByRole('button', { name: 'Try it' })[0])
    fireEvent.click(screen.getByRole('button', { name: 'Check arguments' }))
    expect(await screen.findByText('Required.')).toBeInTheDocument()

    fireEvent.change(screen.getByLabelText('query'), { target: { value: 'roadmap' } })
    fireEvent.click(screen.getByRole('button', { name: 'Check arguments' }))
    const preview = await screen.findByLabelText('Request preview')
    expect(JSON.parse(preview.textContent ?? '')).toEqual({
      jsonrpc: '2.0',
      method: 'tools/call',
      params: { name: 'search', arguments: { query: 'roadmap' } },
    })
    expect(createConnector).not.toHaveBeenCalled()
  })
})
