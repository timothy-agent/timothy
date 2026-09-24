import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdminAgent, AdminConnector, AdminRoute, Automation } from '../api/types'
import { agentID, makeAutomation, makeTemplate, makeTrigger } from '../components/automations/testFixtures'
import { TooltipProvider } from '../components/ui/tooltip'
import { localInputToIso } from '../lib/datetimeLocal'
import { AutomationEditor, type EditorLocationState } from './AutomationEditor'

vi.mock('../api/client', () => ({
  createAutomation: vi.fn(),
  patchAutomation: vi.fn(),
  getAutomation: vi.fn(),
  listDestinations: vi.fn(),
  listConnectors: vi.fn(),
  listAgents: vi.fn(),
  listRoutes: vi.fn(),
  uploadAttachment: vi.fn(),
}))

vi.mock('sonner', () => ({ toast: { error: vi.fn(), success: vi.fn() } }))

import { toast } from 'sonner'
import {
  createAutomation,
  getAutomation,
  listAgents,
  listConnectors,
  listDestinations,
  listRoutes,
  patchAutomation,
} from '../api/client'

const agent = { id: agentID, name: 'briefing', enabled: true, is_default: true } as AdminAgent
const routes: AdminRoute[] = [{ name: 'careful', strategy: 'ordered', enabled: true, chain: [] }]
const connectorID = '0000000c-0000-0000-0000-000000000001'
const github: AdminConnector = {
  id: connectorID,
  name: 'gh-main',
  kind: 'github',
  config: {},
  credential_ref: 'GH_PAT',
  enabled: true,
  sensitive: false,
}

function renderAt(path: string, state?: EditorLocationState) {
  const router = createMemoryRouter(
    [
      { path: '/automations', element: <div>automations page</div> },
      { path: '/automations/new', element: <AutomationEditor mode="create" /> },
      { path: '/automations/:id/edit', element: <AutomationEditor mode="edit" /> },
      { path: '/automations/:id', element: <div>automation detail page</div> },
    ],
    { initialEntries: [{ pathname: path, state }] },
  )
  const result = render(
    <TooltipProvider>
      <RouterProvider router={router} />
    </TooltipProvider>,
  )
  return { router, ...result }
}

// wire drops undefined fields, as JSON.stringify does on the way out.
const wire = (v: unknown) => JSON.parse(JSON.stringify(v)) as Record<string, unknown>

async function ready() {
  await waitFor(() => expect(screen.getByLabelText('Agent')).toHaveTextContent('briefing'))
}

function apiError(message: string, code: string) {
  return Object.assign(new Error(message), { status: 400, code })
}

afterEach(cleanup)
beforeEach(() => {
  Element.prototype.scrollIntoView = vi.fn()
  vi.clearAllMocks()
  vi.mocked(listAgents).mockResolvedValue([agent])
  vi.mocked(listRoutes).mockResolvedValue(routes)
  vi.mocked(listDestinations).mockResolvedValue([])
  vi.mocked(listConnectors).mockResolvedValue([github])
  vi.mocked(createAutomation).mockResolvedValue({ id: 'new1' })
})

describe('AutomationEditor create', () => {
  it('shows the breadcrumb and title', async () => {
    renderAt('/automations/new')
    expect(screen.getByRole('heading', { level: 1, name: 'New automation' })).toBeInTheDocument()
    const crumbs = screen.getByRole('navigation', { name: 'Breadcrumb' })
    expect(within(crumbs).getByRole('link', { name: 'Automations' })).toHaveAttribute('href', '/automations')
    await ready()
  })

  it('posts the exact payload with defaults and no expires_at', async () => {
    const { router } = renderAt('/automations/new')
    await ready()
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'weekly-digest' } })
    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: '  Summarize the week  ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create automation' }))

    await waitFor(() => expect(createAutomation).toHaveBeenCalled())
    const input = wire(vi.mocked(createAutomation).mock.calls[0][0])
    expect(input).toEqual({
      name: 'weekly-digest',
      agent_id: agentID,
      action: { kind: 'mission', mission: { goal: 'Summarize the week', kind: 'general', auto_approve_tools: true, light: false } },
      triggers: [{ kind: 'cron', config: { expr: '0 7 * * *' }, enabled: true }],
      concurrency: 'skip',
      max_concurrent: 1,
      max_runs_per_hour: 6,
      continuity: true,
      notes_enabled: true,
    })
    expect(input).not.toHaveProperty('expires_at')
    expect(toast.success).toHaveBeenCalledWith('Automation created')
    await waitFor(() => expect(router.state.location.pathname).toBe('/automations/new1'))
  })

  it('sends triggers, advanced fields and an expiry', async () => {
    renderAt('/automations/new')
    await ready()
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'pr-review' } })
    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'Review PRs' } })
    fireEvent.change(screen.getByLabelText(/^Expires/), { target: { value: '2026-10-01T09:30' } })

    fireEvent.click(screen.getByRole('button', { name: 'Add trigger' }))
    const second = screen.getByRole('group', { name: 'Trigger 2' })
    fireEvent.click(within(second).getByLabelText('Kind'))
    fireEvent.click(await screen.findByRole('option', { name: 'Manual' }))

    fireEvent.click(screen.getByRole('button', { name: 'Advanced' }))
    fireEvent.click(await screen.findByRole('radio', { name: 'Parallel' }))
    fireEvent.change(screen.getByLabelText('Max concurrent runs'), { target: { value: '3' } })
    fireEvent.change(screen.getByLabelText('Max runs per hour'), { target: { value: '12' } })
    fireEvent.click(screen.getByRole('switch', { name: 'Pass the last result forward' }))
    fireEvent.click(screen.getByRole('switch', { name: 'Notes' }))
    fireEvent.click(screen.getByRole('button', { name: 'Create automation' }))

    await waitFor(() => expect(createAutomation).toHaveBeenCalled())
    expect(wire(vi.mocked(createAutomation).mock.calls[0][0])).toMatchObject({
      triggers: [
        { kind: 'cron', config: { expr: '0 7 * * *' }, enabled: true },
        { kind: 'manual', config: {}, enabled: true },
      ],
      concurrency: 'parallel',
      max_concurrent: 3,
      max_runs_per_hour: 12,
      continuity: false,
      notes_enabled: false,
      expires_at: localInputToIso('2026-10-01T09:30'),
    })
  })

  it('enables max concurrent only for parallel', async () => {
    renderAt('/automations/new')
    await ready()
    fireEvent.click(screen.getByRole('button', { name: 'Advanced' }))
    const max = await screen.findByLabelText('Max concurrent runs')
    expect(max).toBeDisabled()
    fireEvent.click(screen.getByRole('radio', { name: 'Parallel' }))
    expect(max).toBeEnabled()
    fireEvent.click(screen.getByRole('radio', { name: 'Skip' }))
    expect(max).toBeDisabled()
  })

  it('warns about queueing behind a cron trigger', async () => {
    renderAt('/automations/new')
    await ready()
    fireEvent.click(screen.getByRole('button', { name: 'Advanced' }))
    fireEvent.click(await screen.findByRole('radio', { name: 'Queue' }))
    expect(screen.getByText(/Queued runs start only after the active run finishes/)).toBeInTheDocument()

    fireEvent.click(screen.getByLabelText('Kind'))
    fireEvent.click(await screen.findByRole('option', { name: 'Manual' }))
    expect(screen.queryByText(/Queued runs start only after/)).toBeNull()
  })

  it('blocks submit and shows errors for a missing name and goal', async () => {
    renderAt('/automations/new')
    await ready()
    fireEvent.click(screen.getByRole('button', { name: 'Create automation' }))
    expect(await screen.findByText('Enter a name.')).toBeInTheDocument()
    expect(screen.getByText('Enter a goal.')).toBeInTheDocument()
    expect(createAutomation).not.toHaveBeenCalled()
  })

  it('blocks submit on a bad cron shape', async () => {
    renderAt('/automations/new')
    await ready()
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'x' } })
    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'g' } })
    fireEvent.change(screen.getByLabelText('Cron expression'), { target: { value: 'soon' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create automation' }))
    expect(screen.getByText(/Use five fields/)).toBeInTheDocument()
    expect(createAutomation).not.toHaveBeenCalled()
  })

  it('shows name_conflict under the name field', async () => {
    vi.mocked(createAutomation).mockRejectedValue(apiError('an automation with this name already exists', 'name_conflict'))
    renderAt('/automations/new')
    await ready()
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'dup' } })
    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'g' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create automation' }))
    expect(await screen.findByText('An automation with this name already exists.')).toBeInTheDocument()
    expect(toast.error).not.toHaveBeenCalled()
  })

  it('shows bad_cron under the trigger the server names', async () => {
    vi.mocked(createAutomation).mockRejectedValue(
      apiError('triggers[1]: invalid cron expression: end of range (61) above maximum (59)', 'bad_cron'),
    )
    renderAt('/automations/new')
    await ready()
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'x' } })
    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'g' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add trigger' }))
    const second = screen.getByRole('group', { name: 'Trigger 2' })
    fireEvent.change(within(second).getByLabelText('Cron expression'), { target: { value: '0-61 * * * *' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create automation' }))
    expect(await within(second).findByRole('alert')).toHaveTextContent('invalid cron expression: end of range (61)')
    expect(within(screen.getByRole('group', { name: 'Trigger 1' })).queryByRole('alert')).toBeNull()
  })

  it('toasts any other error', async () => {
    vi.mocked(createAutomation).mockRejectedValue(apiError('unknown agent_id', 'bad_request'))
    renderAt('/automations/new')
    await ready()
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'x' } })
    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'g' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create automation' }))
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Could not create automation', { description: 'unknown agent_id' }),
    )
  })

  it('offers workflow actions disabled', async () => {
    renderAt('/automations/new')
    await ready()
    expect(screen.getByRole('radio', { name: 'Workflow (Phase 4)' })).toBeDisabled()
  })

  it('cancels back to the list', async () => {
    const { router } = renderAt('/automations/new')
    await ready()
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(router.state.location.pathname).toBe('/automations')
  })
})

describe('AutomationEditor prefill', () => {
  it('seeds from a gallery template', async () => {
    const template = makeTemplate({ notes_enabled: false, concurrency: 'queue' })
    renderAt('/automations/new', { template })
    await ready()
    expect(screen.getByLabelText('Name')).toHaveValue('Review new pull requests')
    expect(screen.getByLabelText('Goal')).toHaveValue('Review {{event.pr_url}}')
    expect(screen.getByLabelText('Cron expression')).toHaveValue('0 * * * *')
    fireEvent.click(screen.getByRole('button', { name: 'Create automation' }))
    await waitFor(() => expect(createAutomation).toHaveBeenCalled())
    expect(wire(vi.mocked(createAutomation).mock.calls[0][0])).toMatchObject({
      name: 'Review new pull requests',
      description: 'Reads each new PR and posts a summary.',
      triggers: [{ kind: 'cron', config: { expr: '0 * * * *' }, enabled: true }],
      concurrency: 'queue',
      notes_enabled: false,
    })
  })

  it('seeds from the mission form prefill with its agent', async () => {
    const other = { id: '00000000-0000-0000-0000-00000000a002', name: 'researcher', enabled: true } as AdminAgent
    vi.mocked(listAgents).mockResolvedValue([agent, other])
    renderAt('/automations/new', {
      prefill: {
        action: { kind: 'mission', mission: { goal: 'Summarize my inbox', kind: 'general', route: 'careful', auto_approve_tools: true } },
        agent_id: other.id,
      },
    })
    await waitFor(() => expect(screen.getByLabelText('Agent')).toHaveTextContent('researcher'))
    expect(screen.getByLabelText('Goal')).toHaveValue('Summarize my inbox')
    expect(screen.getByLabelText(/^Route/)).toHaveTextContent('careful')
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'inbox' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create automation' }))
    await waitFor(() => expect(createAutomation).toHaveBeenCalled())
    expect(wire(vi.mocked(createAutomation).mock.calls[0][0])).toMatchObject({
      agent_id: other.id,
      action: { kind: 'mission', mission: { goal: 'Summarize my inbox', route: 'careful' } },
    })
  })
})

describe('AutomationEditor event triggers', () => {
  async function fillBasics() {
    await ready()
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'pr-review' } })
    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'Review {{event.url}}' } })
  }

  async function pickKind(name: string) {
    fireEvent.click(screen.getByLabelText('Kind'))
    fireEvent.click(await screen.findByRole('option', { name }))
  }

  it('posts a connector event trigger', async () => {
    renderAt('/automations/new')
    await fillBasics()
    await pickKind('Connector event')
    fireEvent.click(screen.getByLabelText('Connector'))
    fireEvent.click(await screen.findByRole('option', { name: 'gh-main' }))
    fireEvent.change(screen.getByLabelText('Repository'), { target: { value: 'octo/timothy' } })
    fireEvent.click(screen.getByRole('checkbox', { name: /pr\.opened/ }))
    const labels = screen.getByLabelText('Labels optional')
    fireEvent.change(labels, { target: { value: 'needs-review' } })
    fireEvent.keyDown(labels, { key: 'Enter' })
    fireEvent.click(screen.getByRole('button', { name: 'Create automation' }))

    await waitFor(() => expect(createAutomation).toHaveBeenCalled())
    expect(wire(vi.mocked(createAutomation).mock.calls[0][0]).triggers).toEqual([
      {
        kind: 'connector_event',
        config: { connector_id: connectorID, repo: 'octo/timothy', events: ['pr.opened'], labels: ['needs-review'] },
        enabled: true,
      },
    ])
  })

  it('posts a webhook trigger with its secret name and tool allowlist', async () => {
    renderAt('/automations/new')
    await fillBasics()
    await pickKind('Webhook')
    fireEvent.click(screen.getByRole('radio', { name: 'Generic' }))
    fireEvent.change(screen.getByLabelText('Signing secret'), { target: { value: 'HOOK_KEY' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add filter' }))
    fireEvent.change(screen.getByLabelText('Filter 1 path'), { target: { value: 'action' } })
    fireEvent.change(screen.getByLabelText('Filter 1 equals'), { target: { value: 'opened' } })
    fireEvent.click(screen.getByRole('button', { name: 'Tool allowlist, trigger 1' }))
    const tools = screen.getByLabelText('Tools optional')
    fireEvent.change(tools, { target: { value: 'search_mail' } })
    fireEvent.keyDown(tools, { key: 'Enter' })
    fireEvent.click(screen.getByRole('button', { name: 'Create automation' }))

    await waitFor(() => expect(createAutomation).toHaveBeenCalled())
    expect(wire(vi.mocked(createAutomation).mock.calls[0][0]).triggers).toEqual([
      {
        kind: 'webhook',
        config: { scheme: 'generic', filters: [{ path: 'action', equals: 'opened' }] },
        credential_ref: 'HOOK_KEY',
        enabled: true,
        tool_allowlist: ['search_mail'],
      },
    ])
  })

  it('blocks submit on an incomplete event trigger', async () => {
    renderAt('/automations/new')
    await fillBasics()
    await pickKind('Webhook')
    fireEvent.click(screen.getByRole('button', { name: 'Create automation' }))
    expect(await screen.findByText('Enter the signing secret name.')).toBeInTheDocument()
    expect(createAutomation).not.toHaveBeenCalled()
  })

  it('shows a server trigger error under the trigger it names', async () => {
    vi.mocked(createAutomation).mockRejectedValue(
      apiError('triggers[0]: webhook trigger scheme must be "github" or "generic"', 'bad_request'),
    )
    renderAt('/automations/new')
    await fillBasics()
    await pickKind('Webhook')
    fireEvent.change(screen.getByLabelText('Signing secret'), { target: { value: 'HOOK_KEY' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create automation' }))
    const group = screen.getByRole('group', { name: 'Trigger 1' })
    expect(await within(group).findByText('webhook trigger scheme must be "github" or "generic"')).toBeInTheDocument()
    expect(toast.error).not.toHaveBeenCalled()
  })

  it('switches the goal hint with the trigger kinds', async () => {
    renderAt('/automations/new')
    await ready()
    expect(screen.getByText('Use {{event.pr_url}} or {{notes.name}} to insert trigger data or notes.')).toBeInTheDocument()
    await pickKind('Connector event')
    expect(screen.getByText(/\{\{event\.repo\}\}.*\{\{notes\.name\}\}/)).toBeInTheDocument()
    await pickKind('Webhook')
    expect(screen.getByText(/\{\{event\.delivery\}\}/)).toBeInTheDocument()
    expect(screen.queryByText(/\{\{event\.repo\}\}/)).toBeNull()
  })

  it('loads connector event and webhook triggers and keeps their ids', async () => {
    const stored = makeAutomation({
      triggers: [
        makeTrigger({
          id: 't5',
          kind: 'connector_event',
          config: { connector_id: connectorID, repo: 'octo/timothy', events: ['pr.opened'], labels: [] },
        }),
        makeTrigger({
          id: 't6',
          kind: 'webhook',
          config: { scheme: 'github', filters: [{ path: '$.action', equals: 'opened' }] },
          credential_ref: 'HOOK_KEY',
          tool_allowlist: ['search_mail'],
        }),
      ],
    })
    vi.mocked(getAutomation).mockResolvedValue(stored)
    vi.mocked(patchAutomation).mockResolvedValue(stored)
    renderAt('/automations/s1/edit')
    await ready()
    expect(screen.getByLabelText('Repository')).toHaveValue('octo/timothy')
    expect(screen.getByRole('checkbox', { name: /pr\.opened/ })).toBeChecked()
    expect(screen.getByLabelText('Signing secret')).toHaveValue('HOOK_KEY')
    expect(screen.getByLabelText('Filter 1 path')).toHaveValue('action')
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() => expect(patchAutomation).toHaveBeenCalled())
    expect(wire(vi.mocked(patchAutomation).mock.calls[0][1]).triggers).toEqual([
      { id: 't5', kind: 'connector_event', config: { connector_id: connectorID, repo: 'octo/timothy', events: ['pr.opened'] }, enabled: true },
      {
        id: 't6',
        kind: 'webhook',
        config: { scheme: 'github', filters: [{ path: 'action', equals: 'opened' }] },
        credential_ref: 'HOOK_KEY',
        enabled: true,
        tool_allowlist: ['search_mail'],
      },
    ])
  })
})

describe('AutomationEditor edit', () => {
  const stored: Automation = makeAutomation({
    description: 'weekly summary',
    expires_at: '2026-08-01T12:30:00Z',
    triggers: [makeTrigger({ id: 't1', tool_allowlist: ['search_mail'] }), makeTrigger({ id: 't2', kind: 'manual', config: {} })],
  })

  beforeEach(() => {
    vi.mocked(getAutomation).mockResolvedValue(stored)
    vi.mocked(patchAutomation).mockResolvedValue(stored)
  })

  it('shows the edit breadcrumb', async () => {
    renderAt('/automations/s1/edit')
    await ready()
    const crumbs = screen.getByRole('navigation', { name: 'Breadcrumb' })
    expect(within(crumbs).getByRole('link', { name: 'weekly-digest' })).toHaveAttribute('href', '/automations/s1')
    expect(within(crumbs).getByText('Edit')).toHaveAttribute('aria-current', 'page')
  })

  it('loads the automation, keeps trigger ids and omits an untouched expiry', async () => {
    const { router } = renderAt('/automations/s1/edit')
    await ready()
    expect(screen.getByLabelText('Name')).toHaveValue('weekly-digest')
    expect(screen.getByRole('switch', { name: 'Enabled' })).toBeChecked()
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() => expect(patchAutomation).toHaveBeenCalled())
    const [id, patch] = vi.mocked(patchAutomation).mock.calls[0]
    expect(id).toBe('s1')
    const body = wire(patch)
    expect(body).toMatchObject({
      name: 'weekly-digest',
      description: 'weekly summary',
      agent_id: agentID,
      enabled: true,
      triggers: [
        { id: 't1', kind: 'cron', config: { expr: '0 8 * * 1-5' }, enabled: true, tool_allowlist: ['search_mail'] },
        { id: 't2', kind: 'manual', config: {}, enabled: true },
      ],
    })
    expect(body).not.toHaveProperty('expires_at')
    expect(toast.success).toHaveBeenCalledWith('Automation saved')
    await waitFor(() => expect(router.state.location.pathname).toBe('/automations/s1'))
  })

  it('sends expires_at null when the expiry is cleared', async () => {
    renderAt('/automations/s1/edit')
    await ready()
    fireEvent.click(screen.getByRole('button', { name: 'Clear' }))
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(patchAutomation).toHaveBeenCalled())
    expect(vi.mocked(patchAutomation).mock.calls[0][1]).toHaveProperty('expires_at', null)
  })

  it('shows the stored expiry in local time and sends a changed one as UTC', async () => {
    renderAt('/automations/s1/edit')
    await ready()
    const input = screen.getByLabelText(/^Expires/) as HTMLInputElement
    expect(localInputToIso(input.value)).toBe('2026-08-01T12:30:00.000Z')
    fireEvent.change(input, { target: { value: '2026-09-01T10:00' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(patchAutomation).toHaveBeenCalled())
    expect(vi.mocked(patchAutomation).mock.calls[0][1]).toHaveProperty('expires_at', localInputToIso('2026-09-01T10:00'))
  })

  it('sends a disable from the enabled switch', async () => {
    renderAt('/automations/s1/edit')
    await ready()
    fireEvent.click(screen.getByRole('switch', { name: 'Enabled' }))
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(patchAutomation).toHaveBeenCalled())
    expect(vi.mocked(patchAutomation).mock.calls[0][1]).toHaveProperty('enabled', false)
  })

  it('toasts a failed save', async () => {
    vi.mocked(patchAutomation).mockRejectedValue(apiError('boom', 'automations_failed'))
    renderAt('/automations/s1/edit')
    await ready()
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith('Could not save automation', { description: 'boom' }))
  })

  it('renders not found for an unknown automation', async () => {
    vi.mocked(getAutomation).mockRejectedValue(new Error('not found'))
    renderAt('/automations/nope/edit')
    expect(await screen.findByText('Automation not found.')).toBeInTheDocument()
  })
})
