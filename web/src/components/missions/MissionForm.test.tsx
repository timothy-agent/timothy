import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdminAgent, AdminConnector, AdminRoute, GitHubRepo, Mission, Schedule } from '../../api/types'
import { defaultRouteLabel, MissionForm } from './MissionForm'

vi.mock('../../api/client', () => ({
  classifyMission: vi.fn(),
  createMission: vi.fn(),
  createSchedule: vi.fn(),
  patchSchedule: vi.fn(),
  listAgents: vi.fn(),
  listRoutes: vi.fn(),
  listConnectors: vi.fn(),
  listConnectorRepos: vi.fn(),
  createConnectorRepo: vi.fn(),
  getSettings: vi.fn(),
  getMissionExecutorOptions: vi.fn(),
  testConnector: vi.fn().mockResolvedValue({ ok: true }),
  uploadAttachment: vi.fn(),
  listDestinations: vi.fn().mockResolvedValue([]),
}))

import {
  classifyMission,
  createConnectorRepo,
  createMission,
  createSchedule,
  getMissionExecutorOptions,
  getSettings,
  listAgents,
  listConnectorRepos,
  listConnectors,
  listDestinations,
  listRoutes,
  patchSchedule,
  uploadAttachment,
} from '../../api/client'
import type { Destination } from '../../api/types'

// renderForm wraps MissionForm in a MemoryRouter — the repository
// section's "no connectors" hint links to Settings → Connectors, which
// needs router context even when that section never renders.
function renderForm(el: React.ReactElement) {
  return render(<MemoryRouter>{el}</MemoryRouter>)
}

const routes: AdminRoute[] = [
  { name: 'default', strategy: 'ordered', enabled: true, chain: [] },
  { name: 'careful', strategy: 'ordered', enabled: true, chain: [] },
  { name: 'disabled-route', strategy: 'ordered', enabled: false, chain: [] },
]

const schedule: Schedule = {
  id: 's1',
  name: 'weekly-digest',
  cron: '0 8 * * 1-5',
  mission_template: {
    goal: 'Summarize the week',
    kind: 'general',
    auto_approve_safe: true,
    review_route: 'default',
  },
  enabled: true,
  expires_at: '2026-08-01T12:30:00Z',
  next_run: '2026-07-27T08:00:00Z',
  created_at: '2026-07-01T00:00:00Z',
  updated_at: '2026-07-01T00:00:00Z',
  pending_fire: false,
}

function makeAgent(overrides: Partial<AdminAgent> = {}): AdminAgent {
  return {
    id: 'a1',
    name: 'agent',
    description: '',
    prompt_overlay: '',
    route: '',
    skills: [],
    tools: [],
    memory: false,
    is_default: false,
    enabled: true,
    ...overrides,
  }
}

describe('defaultRouteLabel', () => {
  it("uses the agent's own route when the agent has one", () => {
    const agent = makeAgent({ route: 'careful' })
    expect(defaultRouteLabel('general', agent, routes)).toBe('Default (careful)')
    // Takes priority even for coding with a "coding" route present.
    expect(
      defaultRouteLabel('coding', agent, [...routes, { name: 'coding', strategy: 'ordered', enabled: true, chain: [] }]),
    ).toBe('Default (careful)')
  })

  it('falls back to a route literally named "coding" for coding missions when no agent route is set', () => {
    const codingRoutes: AdminRoute[] = [
      ...routes,
      { name: 'coding', strategy: 'ordered', enabled: true, chain: [] },
    ]
    expect(defaultRouteLabel('coding', undefined, codingRoutes)).toBe('Default (coding)')
    expect(defaultRouteLabel('coding', makeAgent({ route: '' }), codingRoutes)).toBe('Default (coding)')
  })

  it('falls back to the route carrying the "default" role otherwise', () => {
    const withDefaultRole: AdminRoute[] = [
      { name: 'default', strategy: 'ordered', enabled: true, chain: [], role: 'default' },
      { name: 'careful', strategy: 'ordered', enabled: true, chain: [] },
    ]
    expect(defaultRouteLabel('general', undefined, withDefaultRole)).toBe('Default (default)')
    // Also applies to a coding mission when no "coding"-named route exists.
    expect(defaultRouteLabel('coding', undefined, withDefaultRole)).toBe('Default (default)')
  })

  it('falls back to plain "Default" when nothing resolves', () => {
    expect(defaultRouteLabel('general', undefined, routes)).toBe('Default')
    expect(defaultRouteLabel('coding', undefined, routes)).toBe('Default')
    expect(defaultRouteLabel('general', undefined, null)).toBe('Default')
  })
})

afterEach(() => {
  cleanup()
  vi.useRealTimers()
})
beforeEach(() => {
  Element.prototype.scrollIntoView = vi.fn()
  vi.clearAllMocks()
  vi.mocked(listAgents).mockResolvedValue([])
  vi.mocked(listRoutes).mockResolvedValue(routes)
  vi.mocked(getSettings).mockResolvedValue({ settings: {}, values: {} })
  vi.mocked(classifyMission).mockResolvedValue({ kind: 'general' })
  vi.mocked(getMissionExecutorOptions).mockResolvedValue([])
  vi.mocked(listConnectors).mockResolvedValue([])
})

describe('MissionForm — create mode, one-off mission', () => {
  it('submits a general mission with the entered goal', async () => {
    vi.mocked(createMission).mockResolvedValue({ id: 'm2' } as Mission)
    const onDone = vi.fn()
    renderForm(<MissionForm mode="create" onDone={onDone} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'Research something new' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create mission' }))

    await waitFor(() =>
      expect(createMission).toHaveBeenCalledWith(
        expect.objectContaining({
          goal: 'Research something new',
          kind: 'general',
          auto_approve_safe: true,
        }),
      ),
    )
    expect(onDone).toHaveBeenCalledWith({ kind: 'mission', id: 'm2' })
  })

  it('preserves multi-line markdown in the goal on submit, trimming only leading/trailing whitespace', async () => {
    vi.mocked(createMission).mockResolvedValue({ id: 'm2' } as Mission)
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    const markdownGoal = '## Plan\n\n- step one\n- step two\n\nDo it **carefully**.'
    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: `  ${markdownGoal}  ` } })
    fireEvent.click(screen.getByRole('button', { name: 'Create mission' }))

    await waitFor(() =>
      expect(createMission).toHaveBeenCalledWith(expect.objectContaining({ goal: markdownGoal })),
    )
  })

  it('sends auto_approve_safe: false when the toggle is unchecked', async () => {
    vi.mocked(createMission).mockResolvedValue({ id: 'm2' } as Mission)
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'Research something new' } })
    fireEvent.click(screen.getByLabelText(/Auto-approve safe tool calls/))
    fireEvent.click(screen.getByRole('button', { name: 'Create mission' }))

    await waitFor(() =>
      expect(createMission).toHaveBeenCalledWith(
        expect.objectContaining({ auto_approve_safe: false }),
      ),
    )
  })

  it('disables submit until a goal is entered', () => {
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    const createButton = screen.getByRole('button', { name: 'Create mission' }) as HTMLButtonElement
    expect(createButton.disabled).toBe(true)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'Fix a bug' } })
    expect(createButton.disabled).toBe(false)
  })

  it('calls onCancel when Cancel is clicked', () => {
    const onCancel = vi.fn()
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={onCancel} />)
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(onCancel).toHaveBeenCalled()
  })

  it('attaching a PDF renders a chip and submits it on the payload', async () => {
    vi.mocked(uploadAttachment).mockResolvedValue({ id: 'att1', mime: 'application/pdf', size_bytes: 100 })
    vi.mocked(createMission).mockResolvedValue({ id: 'm2' } as Mission)
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'Summarize the attached spec' } })
    const file = new File(['%PDF-1.4'], 'spec.pdf', { type: 'application/pdf' })
    const input = document.querySelector('input[type="file"]') as HTMLInputElement
    fireEvent.change(input, { target: { files: [file] } })

    await screen.findByText('spec.pdf')
    fireEvent.click(screen.getByRole('button', { name: 'Create mission' }))

    await waitFor(() =>
      expect(createMission).toHaveBeenCalledWith(
        expect.objectContaining({ attachments: [{ id: 'att1', name: 'spec.pdf' }] }),
      ),
    )
  })

  it('disables submit while an attachment is uploading', async () => {
    let resolveUpload: (v: { id: string; mime: string; size_bytes: number }) => void = () => {}
    vi.mocked(uploadAttachment).mockReturnValue(
      new Promise((resolve) => {
        resolveUpload = resolve
      }),
    )
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'Summarize the attached spec' } })
    const file = new File(['%PDF-1.4'], 'spec.pdf', { type: 'application/pdf' })
    const input = document.querySelector('input[type="file"]') as HTMLInputElement
    fireEvent.change(input, { target: { files: [file] } })

    await screen.findByText('spec.pdf')
    const createButton = screen.getByRole('button', { name: 'Create mission' }) as HTMLButtonElement
    expect(createButton.disabled).toBe(true)

    resolveUpload({ id: 'att1', mime: 'application/pdf', size_bytes: 100 })
    await waitFor(() => expect(createButton.disabled).toBe(false))
  })
})

const destinations: Destination[] = [
  {
    id: 'd1',
    name: 'ops-inbox',
    kind: 'email',
    config: { connector_id: 'c1', to: 'ops@example.com' },
    credential_ref: '',
    enabled: true,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
  },
  {
    id: 'd2',
    name: 'ops-hook',
    kind: 'webhook',
    config: { url: 'https://example.com/hook', format: 'json' },
    credential_ref: '',
    enabled: true,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
  },
]

describe('MissionForm — destinations multi-select', () => {
  it('hides the section when there are no destinations', async () => {
    vi.mocked(listDestinations).mockResolvedValue([])
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    await waitFor(() => expect(listDestinations).toHaveBeenCalled())
    expect(screen.queryByText('Deliver results to')).toBeNull()
  })

  it('renders a checkbox per destination, unchecked by default, and submits the picked ids', async () => {
    vi.mocked(listDestinations).mockResolvedValue(destinations)
    vi.mocked(createMission).mockResolvedValue({ id: 'm2' } as Mission)
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    await screen.findByText('Deliver results to')
    const opsInbox = screen.getByLabelText(/^ops-inbox/) as HTMLInputElement
    const opsHook = screen.getByLabelText(/^ops-hook/) as HTMLInputElement
    expect(opsInbox.checked).toBe(false)
    expect(opsHook.checked).toBe(false)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'Send me the digest' } })
    fireEvent.click(opsInbox)
    fireEvent.click(screen.getByRole('button', { name: 'Create mission' }))

    await waitFor(() =>
      expect(createMission).toHaveBeenCalledWith(
        expect.objectContaining({ destination_ids: ['d1'] }),
      ),
    )
  })

  it('omits destination_ids from the create payload when none are picked', async () => {
    vi.mocked(listDestinations).mockResolvedValue(destinations)
    vi.mocked(createMission).mockResolvedValue({ id: 'm2' } as Mission)
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    await screen.findByText('Deliver results to')
    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'No delivery please' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create mission' }))

    await waitFor(() => expect(createMission).toHaveBeenCalled())
    expect(createMission).toHaveBeenCalledWith(
      expect.not.objectContaining({ destination_ids: expect.anything() }),
    )
  })
})

describe('MissionForm — follow-up', () => {
  it('submits parent_mission_id when given, and seeds kind from initial without locking goal', async () => {
    vi.mocked(createMission).mockResolvedValue({ id: 'm-followup' } as Mission)
    renderForm(
      <MissionForm
        mode="create"
        initial={{ kind: 'coding' }}
        parentMissionId="parent-1"
        onDone={vi.fn()}
        onCancel={vi.fn()}
      />,
    )

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'Continue the work' } })
    expect(screen.getByText('Coding · branches from repo')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Create mission' }))

    await waitFor(() =>
      expect(createMission).toHaveBeenCalledWith(
        expect.objectContaining({
          goal: 'Continue the work',
          kind: 'coding',
          parent_mission_id: 'parent-1',
        }),
      ),
    )
  })

  it('omits parent_mission_id from the create payload for an ordinary mission', async () => {
    vi.mocked(createMission).mockResolvedValue({ id: 'm2' } as Mission)
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'Research something new' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create mission' }))

    await waitFor(() =>
      expect(createMission).toHaveBeenCalledWith(
        expect.objectContaining({ parent_mission_id: undefined }),
      ),
    )
  })
})

describe('MissionForm — kind chip', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('shows a detecting state then the classified kind after the debounce', async () => {
    vi.mocked(classifyMission).mockResolvedValue({ kind: 'coding' })
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'Fix a bug in the repo' } })
    expect(screen.getByText('Detecting…')).toBeInTheDocument()
    expect(classifyMission).not.toHaveBeenCalled()

    await vi.advanceTimersByTimeAsync(600)
    await vi.advanceTimersByTimeAsync(0)

    expect(classifyMission).toHaveBeenCalledWith('Fix a bug in the repo')
    expect(screen.getByText('Coding · branches from repo')).toBeInTheDocument()
  })

  it('debounces repeated goal edits into a single classify call', async () => {
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    const goalInput = screen.getByLabelText('Goal')
    fireEvent.change(goalInput, { target: { value: 'Fix a' } })
    await vi.advanceTimersByTimeAsync(300)
    fireEvent.change(goalInput, { target: { value: 'Fix a bug' } })
    await vi.advanceTimersByTimeAsync(600)

    expect(classifyMission).toHaveBeenCalledTimes(1)
    expect(classifyMission).toHaveBeenCalledWith('Fix a bug')
  })

  it('submits the mission with the classified kind', async () => {
    vi.mocked(classifyMission).mockResolvedValue({ kind: 'coding' })
    vi.mocked(createMission).mockResolvedValue({ id: 'm3' } as Mission)
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'Fix a bug' } })
    await vi.advanceTimersByTimeAsync(600)
    await vi.advanceTimersByTimeAsync(0)

    fireEvent.click(screen.getByRole('button', { name: 'Create mission' }))
    await vi.advanceTimersByTimeAsync(0)
    expect(createMission).toHaveBeenCalledWith(expect.objectContaining({ kind: 'coding' }))
  })

  it('clicking the chip toggles kind and locks it against further auto-detect', async () => {
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'Fix a bug' } })
    await vi.advanceTimersByTimeAsync(600)
    await vi.advanceTimersByTimeAsync(0)
    expect(screen.getByText('General · scratch workspace')).toBeInTheDocument()

    fireEvent.click(screen.getByText('General · scratch workspace'))
    expect(screen.getByText('Coding · branches from repo')).toBeInTheDocument()

    vi.mocked(classifyMission).mockClear()
    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'Fix a bug in the app' } })
    await vi.advanceTimersByTimeAsync(600)
    await vi.advanceTimersByTimeAsync(0)

    // Locked: the further edit above must not trigger a reclassify.
    expect(classifyMission).not.toHaveBeenCalled()
    expect(screen.getByText('Coding · branches from repo')).toBeInTheDocument()
  })
})

describe('MissionForm — harness select placement', () => {
  it('shows the harness select in the main form body for a coding mission, without expanding Advanced', async () => {
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'g' } })
    fireEvent.click(await screen.findByText('General · scratch workspace'))
    expect(screen.getByText('Coding · branches from repo')).toBeInTheDocument()

    expect(screen.getByLabelText('Harness')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Hide advanced options' })).toBeNull()
  })

  it('omits the harness select for a general mission', async () => {
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'g' } })
    expect(await screen.findByText('General · scratch workspace')).toBeInTheDocument()
    expect(screen.queryByLabelText('Harness')).toBeNull()
  })

  it('submits the picked harness for a coding mission', async () => {
    vi.mocked(createMission).mockResolvedValue({ id: 'm5' } as Mission)
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'g' } })
    fireEvent.click(await screen.findByText('General · scratch workspace'))
    fireEvent.click(screen.getByLabelText('Harness'))
    fireEvent.click(await screen.findByText('Claude Code'))
    fireEvent.click(screen.getByRole('button', { name: 'Create mission' }))

    await waitFor(() =>
      expect(createMission).toHaveBeenCalledWith(expect.objectContaining({ harness: 'claude-cli' })),
    )
  })
})

describe('MissionForm — environment select', () => {
  it('shows the environment select for a coding mission, defaulted to Auto-detect', async () => {
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'g' } })
    fireEvent.click(await screen.findByText('General · scratch workspace'))

    expect(screen.getByLabelText('Environment')).toBeInTheDocument()
    expect(screen.getByLabelText('Environment')).toHaveTextContent('Auto-detect')
  })

  it('omits the environment select for a general mission', async () => {
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'g' } })
    expect(await screen.findByText('General · scratch workspace')).toBeInTheDocument()
    expect(screen.queryByLabelText('Environment')).toBeNull()
  })

  it('submits the picked environment for a coding mission, and omits it when left on Auto-detect', async () => {
    vi.mocked(createMission).mockResolvedValue({ id: 'm6' } as Mission)
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'g' } })
    fireEvent.click(await screen.findByText('General · scratch workspace'))
    fireEvent.click(screen.getByLabelText('Environment'))
    fireEvent.click(await screen.findByText('Go'))
    fireEvent.click(screen.getByRole('button', { name: 'Create mission' }))

    await waitFor(() =>
      expect(createMission).toHaveBeenCalledWith(expect.objectContaining({ environment: 'go' })),
    )
  })

  it('omits environment from the create payload when left on Auto-detect', async () => {
    vi.mocked(createMission).mockResolvedValue({ id: 'm7' } as Mission)
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'g' } })
    fireEvent.click(await screen.findByText('General · scratch workspace'))
    fireEvent.click(screen.getByRole('button', { name: 'Create mission' }))

    await waitFor(() =>
      expect(createMission).toHaveBeenCalledWith(expect.objectContaining({ environment: undefined })),
    )
  })
})

describe('MissionForm — git strategy overrides', () => {
  it('shows branch pattern and commit style in Advanced for a coding mission', async () => {
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'g' } })
    fireEvent.click(await screen.findByText('General · scratch workspace'))
    fireEvent.click(screen.getByRole('button', { name: 'Show advanced options' }))

    expect(screen.getByLabelText('Branch pattern')).toBeInTheDocument()
    expect(screen.getByLabelText('Commit style')).toBeInTheDocument()
    expect(screen.getByLabelText('Commit style')).toHaveTextContent('Default (from settings)')
  })

  it('omits branch pattern and commit style for a general mission', async () => {
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'g' } })
    expect(await screen.findByText('General · scratch workspace')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Show advanced options' }))

    expect(screen.queryByLabelText('Branch pattern')).toBeNull()
    expect(screen.queryByLabelText('Commit style')).toBeNull()
  })

  it('submits a typed branch pattern and picked commit style for a coding mission', async () => {
    vi.mocked(createMission).mockResolvedValue({ id: 'm8' } as Mission)
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'g' } })
    fireEvent.click(await screen.findByText('General · scratch workspace'))
    fireEvent.click(screen.getByRole('button', { name: 'Show advanced options' }))
    fireEvent.change(screen.getByLabelText('Branch pattern'), {
      target: { value: '{type}/{login}/{slug}' },
    })
    fireEvent.click(screen.getByLabelText('Commit style'))
    fireEvent.click(await screen.findByText('Plain'))
    fireEvent.click(screen.getByRole('button', { name: 'Create mission' }))

    await waitFor(() =>
      expect(createMission).toHaveBeenCalledWith(
        expect.objectContaining({ branch_pattern: '{type}/{login}/{slug}', commit_style: 'plain' }),
      ),
    )
  })

  it('omits branch pattern and commit style from the create payload when left on defaults', async () => {
    vi.mocked(createMission).mockResolvedValue({ id: 'm9' } as Mission)
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'g' } })
    fireEvent.click(await screen.findByText('General · scratch workspace'))
    fireEvent.click(screen.getByRole('button', { name: 'Create mission' }))

    await waitFor(() =>
      expect(createMission).toHaveBeenCalledWith(
        expect.objectContaining({ branch_pattern: undefined, commit_style: undefined }),
      ),
    )
  })
})

const githubConnector: AdminConnector = {
  id: 'c1',
  name: 'personal-gh',
  kind: 'github',
  config: {},
  credential_ref: 'GH_PAT',
  enabled: true,
  sensitive: false,
}

const repos: GitHubRepo[] = [
  {
    full_name: 'octocat/hello-world',
    private: false,
    default_branch: 'main',
    html_url: 'https://github.com/octocat/hello-world',
    clone_url: 'https://github.com/octocat/hello-world.git',
    pushed_at: '2026-08-01T00:00:00Z',
  },
  {
    full_name: 'octocat/secret-project',
    private: true,
    default_branch: 'main',
    html_url: 'https://github.com/octocat/secret-project',
    clone_url: 'https://github.com/octocat/secret-project.git',
    pushed_at: '2026-07-01T00:00:00Z',
  },
]

// Drives the goal + coding-kind selection every repository-section test
// needs before the section itself renders.
async function toCodingMission() {
  fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'g' } })
  fireEvent.click(await screen.findByText('General · scratch workspace'))
  expect(screen.getByText('Coding · branches from repo')).toBeInTheDocument()
}

describe('MissionForm — repository source', () => {
  it('defaults to None and omits repo_url/connector_id from the create payload', async () => {
    vi.mocked(createMission).mockResolvedValue({ id: 'm8' } as Mission)
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    await toCodingMission()
    expect(screen.getByRole('button', { name: 'None' })).toHaveAttribute('aria-pressed', 'true')
    fireEvent.click(screen.getByRole('button', { name: 'Create mission' }))

    await waitFor(() =>
      expect(createMission).toHaveBeenCalledWith(
        expect.objectContaining({ repo_url: undefined, connector_id: undefined }),
      ),
    )
  })

  it('hides the repository section for a general mission', async () => {
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'g' } })
    expect(await screen.findByText('General · scratch workspace')).toBeInTheDocument()
    expect(screen.queryByText('Repository')).toBeNull()
  })

  it('shows a hint linking to Settings → Connectors when no github connector exists', async () => {
    vi.mocked(listConnectors).mockResolvedValue([])
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    await toCodingMission()
    fireEvent.click(screen.getByRole('button', { name: 'GitHub' }))

    expect(await screen.findByText(/No GitHub connectors configured yet/)).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /Add one in Settings/ })).toHaveAttribute(
      'href',
      '/settings/connectors',
    )
  })

  it('lists connector repos, filters them, and submits the picked clone_url + connector_id', async () => {
    vi.mocked(listConnectors).mockResolvedValue([githubConnector])
    vi.mocked(listConnectorRepos).mockResolvedValue(repos)
    vi.mocked(createMission).mockResolvedValue({ id: 'm9' } as Mission)
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    await toCodingMission()
    fireEvent.click(screen.getByRole('button', { name: 'GitHub' }))
    fireEvent.click(await screen.findByLabelText('Connector'))
    fireEvent.click(await screen.findByText('personal-gh'))

    await waitFor(() => expect(listConnectorRepos).toHaveBeenCalledWith('c1'))
    fireEvent.click(await screen.findByRole('button', { name: 'Choose a repository' }))
    expect(await screen.findByText('octocat/hello-world')).toBeInTheDocument()
    expect(screen.getByText('octocat/secret-project')).toBeInTheDocument()

    fireEvent.change(screen.getByPlaceholderText('Filter repositories…'), {
      target: { value: 'secret' },
    })
    expect(screen.queryByText('octocat/hello-world')).toBeNull()
    fireEvent.click(screen.getByText('octocat/secret-project'))

    fireEvent.click(screen.getByRole('button', { name: 'Create mission' }))
    await waitFor(() =>
      expect(createMission).toHaveBeenCalledWith(
        expect.objectContaining({
          repo_url: 'https://github.com/octocat/secret-project.git',
          connector_id: 'c1',
        }),
      ),
    )
    expect(createConnectorRepo).not.toHaveBeenCalled()
  })

  it('surfaces a repo list load error inline', async () => {
    vi.mocked(listConnectors).mockResolvedValue([githubConnector])
    vi.mocked(listConnectorRepos).mockRejectedValue(new Error('bad credentials'))
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    await toCodingMission()
    fireEvent.click(screen.getByRole('button', { name: 'GitHub' }))
    fireEvent.click(await screen.findByLabelText('Connector'))
    fireEvent.click(await screen.findByText('personal-gh'))

    fireEvent.click(await screen.findByRole('button', { name: 'Choose a repository' }))
    expect(await screen.findByText(/Could not load repos: bad credentials/)).toBeInTheDocument()
  })

  it('new-repository flow: creates the repo first, then submits its clone_url', async () => {
    vi.mocked(listConnectors).mockResolvedValue([githubConnector])
    vi.mocked(listConnectorRepos).mockResolvedValue(repos)
    vi.mocked(createConnectorRepo).mockResolvedValue({
      full_name: 'octocat/brand-new',
      private: true,
      default_branch: 'main',
      html_url: 'https://github.com/octocat/brand-new',
      clone_url: 'https://github.com/octocat/brand-new.git',
      pushed_at: '2026-08-05T00:00:00Z',
    })
    vi.mocked(createMission).mockResolvedValue({ id: 'm10' } as Mission)
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    await toCodingMission()
    fireEvent.click(screen.getByRole('button', { name: 'GitHub' }))
    fireEvent.click(await screen.findByLabelText('Connector'))
    fireEvent.click(await screen.findByText('personal-gh'))

    fireEvent.click(await screen.findByLabelText('New repository'))
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'brand-new' } })
    // Private defaults on; leave it checked.
    expect((screen.getByLabelText('Private') as HTMLInputElement).checked).toBe(true)

    fireEvent.click(screen.getByRole('button', { name: 'Create mission' }))

    await waitFor(() =>
      expect(createConnectorRepo).toHaveBeenCalledWith('c1', { name: 'brand-new', private: true }),
    )
    await waitFor(() =>
      expect(createMission).toHaveBeenCalledWith(
        expect.objectContaining({
          repo_url: 'https://github.com/octocat/brand-new.git',
          connector_id: 'c1',
        }),
      ),
    )
  })

  it('disables submit for the GitHub source until a connector and repo (or new-repo name) are chosen', async () => {
    vi.mocked(listConnectors).mockResolvedValue([githubConnector])
    vi.mocked(listConnectorRepos).mockResolvedValue(repos)
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    await toCodingMission()
    fireEvent.click(screen.getByRole('button', { name: 'GitHub' }))

    const submit = screen.getByRole('button', { name: 'Create mission' }) as HTMLButtonElement
    expect(submit.disabled).toBe(true)

    fireEvent.click(await screen.findByLabelText('Connector'))
    fireEvent.click(await screen.findByText('personal-gh'))
    expect(submit.disabled).toBe(true)

    fireEvent.click(await screen.findByRole('button', { name: 'Choose a repository' }))
    fireEvent.click(await screen.findByText('octocat/hello-world'))
    expect(submit.disabled).toBe(false)
  })
})

describe('MissionForm — deployment (on_complete)', () => {
  it('hides the Deployment section until a repo (or new-repo name) is chosen', async () => {
    vi.mocked(listConnectors).mockResolvedValue([githubConnector])
    vi.mocked(listConnectorRepos).mockResolvedValue(repos)
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    await toCodingMission()
    expect(screen.queryByText('Deployment')).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: 'GitHub' }))
    expect(screen.queryByText('Deployment')).toBeNull()

    fireEvent.click(await screen.findByLabelText('Connector'))
    fireEvent.click(await screen.findByText('personal-gh'))
    expect(screen.queryByText('Deployment')).toBeNull()

    fireEvent.click(await screen.findByRole('button', { name: 'Choose a repository' }))
    fireEvent.click(await screen.findByText('octocat/hello-world'))
    expect(await screen.findByText('Deployment')).toBeInTheDocument()
  })

  it('hides the Deployment section for the None repo source', async () => {
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)
    await toCodingMission()
    expect(screen.queryByText('Deployment')).toBeNull()
  })

  it('defaults to Nothing and omits on_complete from the create payload', async () => {
    vi.mocked(listConnectors).mockResolvedValue([githubConnector])
    vi.mocked(listConnectorRepos).mockResolvedValue(repos)
    vi.mocked(createMission).mockResolvedValue({ id: 'm11' } as Mission)
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    await toCodingMission()
    fireEvent.click(screen.getByRole('button', { name: 'GitHub' }))
    fireEvent.click(await screen.findByLabelText('Connector'))
    fireEvent.click(await screen.findByText('personal-gh'))
    fireEvent.click(await screen.findByRole('button', { name: 'Choose a repository' }))
    fireEvent.click(await screen.findByText('octocat/hello-world'))

    fireEvent.click(screen.getByRole('button', { name: 'Create mission' }))
    await waitFor(() =>
      expect(createMission).toHaveBeenCalledWith(expect.objectContaining({ on_complete: undefined })),
    )
  })

  it('submits on_complete="push_pr" when Push and open a PR is chosen', async () => {
    vi.mocked(listConnectors).mockResolvedValue([githubConnector])
    vi.mocked(listConnectorRepos).mockResolvedValue(repos)
    vi.mocked(createMission).mockResolvedValue({ id: 'm12' } as Mission)
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    await toCodingMission()
    fireEvent.click(screen.getByRole('button', { name: 'GitHub' }))
    fireEvent.click(await screen.findByLabelText('Connector'))
    fireEvent.click(await screen.findByText('personal-gh'))
    fireEvent.click(await screen.findByRole('button', { name: 'Choose a repository' }))
    fireEvent.click(await screen.findByText('octocat/hello-world'))

    fireEvent.click(await screen.findByLabelText('Deployment'))
    fireEvent.click(await screen.findByText('Push and open a PR when done'))

    fireEvent.click(screen.getByRole('button', { name: 'Create mission' }))
    await waitFor(() =>
      expect(createMission).toHaveBeenCalledWith(expect.objectContaining({ on_complete: 'push_pr' })),
    )
  })
})

describe('MissionForm — create mode, repeat on schedule', () => {
  it('submits a schedule with the slugified default name, preset cron, and general kind', async () => {
    vi.mocked(createSchedule).mockResolvedValue({ id: 'sc1' })
    const onDone = vi.fn()
    renderForm(<MissionForm mode="create" onDone={onDone} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), {
      target: { value: 'Check the news every morning' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Repeat on schedule' }))
    fireEvent.click(screen.getByRole('button', { name: 'Create schedule' }))

    await waitFor(() =>
      expect(createSchedule).toHaveBeenCalledWith(
        expect.objectContaining({
          name: 'check-the-news-every-morning',
          cron: '0 7 * * *',
          mission_template: expect.objectContaining({
            goal: 'Check the news every morning',
            kind: 'general',
            auto_approve_safe: true,
          }),
        }),
      ),
    )
    expect(createMission).not.toHaveBeenCalled()
    expect(onDone).toHaveBeenCalledWith({ kind: 'schedule', id: 'sc1' })
  })

  it('forces kind to general and locks it when repeat turns on with coding selected', async () => {
    vi.useFakeTimers()
    vi.mocked(classifyMission).mockResolvedValue({ kind: 'coding' })
    vi.mocked(createSchedule).mockResolvedValue({ id: 'sc1' })
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'g' } })
    await vi.advanceTimersByTimeAsync(600)
    await vi.advanceTimersByTimeAsync(0)
    expect(screen.getByText('Coding · branches from repo')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Repeat on schedule' }))
    expect(screen.getByText('General · scratch workspace')).toBeInTheDocument()

    vi.useRealTimers()
    fireEvent.click(screen.getByRole('button', { name: 'Create schedule' }))

    await waitFor(() =>
      expect(createSchedule).toHaveBeenCalledWith(
        expect.objectContaining({
          mission_template: expect.objectContaining({ kind: 'general' }),
        }),
      ),
    )
  })

  it('disables the chip toggle while repeating (coding unavailable)', async () => {
    vi.useFakeTimers()
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'g' } })
    fireEvent.click(screen.getByRole('button', { name: 'Repeat on schedule' }))
    await vi.advanceTimersByTimeAsync(600)
    await vi.advanceTimersByTimeAsync(0)

    expect(screen.getByText('General · scratch workspace')).toBeInTheDocument()
    fireEvent.click(screen.getByText('General · scratch workspace'))
    // Still general: the chip toggle no-ops for coding while repeating.
    expect(screen.getByText('General · scratch workspace')).toBeInTheDocument()
    vi.useRealTimers()
  })

  it('blocks submit on a malformed custom cron shape', async () => {
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'g' } })
    fireEvent.click(screen.getByRole('button', { name: 'Repeat on schedule' }))
    fireEvent.click(screen.getAllByRole('combobox')[0])
    fireEvent.click(await screen.findByText('Custom'))
    fireEvent.change(screen.getByLabelText('Cron expression'), { target: { value: 'bad cron' } })

    const submitButton = screen.getByRole('button', { name: 'Create schedule' }) as HTMLButtonElement
    expect(submitButton.disabled).toBe(true)
    expect(createSchedule).not.toHaveBeenCalled()
  })

  it('cascades the picked agent onto review route the same as a one-off mission', async () => {
    vi.mocked(listAgents).mockResolvedValue([
      {
        id: 'a1',
        name: 'briefing',
        description: '',
        prompt_overlay: '',
        route: '',
        skills: [],
        tools: [],
        memory: false,
        is_default: false,
        enabled: true,
        review_route: 'careful',
      },
    ])
    vi.mocked(createSchedule).mockResolvedValue({ id: 'sc1' })
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(await screen.findByLabelText('Goal'), { target: { value: 'g' } })
    fireEvent.click(screen.getByRole('button', { name: 'Repeat on schedule' }))

    // Combobox order while repeating: Runs (cron preset), then Agent.
    fireEvent.click(screen.getAllByRole('combobox')[1])
    fireEvent.click(await screen.findByText('briefing'))
    fireEvent.click(screen.getByRole('button', { name: 'Show advanced options' }))

    expect(screen.getByLabelText('Review route')).toHaveTextContent('careful')
  })

  it('renders route selects fed from the live routes list, excluding disabled routes', async () => {
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(await screen.findByLabelText('Goal'), { target: { value: 'g' } })
    fireEvent.click(screen.getByRole('button', { name: 'Show advanced options' }))
    await screen.findByLabelText('Review route')

    fireEvent.click(screen.getByLabelText('Route'))
    expect(await screen.findByText('careful')).toBeInTheDocument()
    expect(screen.queryByText('disabled-route')).not.toBeInTheDocument()
  })

  it('submits the picked route and review route', async () => {
    vi.mocked(createMission).mockResolvedValue({ id: 'm4' } as Mission)
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'Fix a bug' } })
    fireEvent.click(screen.getByRole('button', { name: 'Show advanced options' }))
    await screen.findByLabelText('Review route')

    fireEvent.click(screen.getByLabelText('Route'))
    fireEvent.click(await screen.findByText('careful'))
    fireEvent.click(screen.getByRole('button', { name: 'Create mission' }))

    await waitFor(() =>
      expect(createMission).toHaveBeenCalledWith(expect.objectContaining({ route: 'careful' })),
    )
  })
})

describe('MissionForm — plan route', () => {
  it('renders the Plan route select in Advanced, defaulted to "Same as execute route"', async () => {
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'g' } })
    fireEvent.click(screen.getByRole('button', { name: 'Show advanced options' }))
    await screen.findByLabelText('Review route')

    expect(screen.getByLabelText('Plan route')).toBeInTheDocument()
    expect(screen.getByLabelText('Plan route')).toHaveTextContent('Same as execute route')
  })

  it('submits plan_route when a route other than the default is picked', async () => {
    vi.mocked(createMission).mockResolvedValue({ id: 'm13' } as Mission)
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'Fix a bug' } })
    fireEvent.click(screen.getByRole('button', { name: 'Show advanced options' }))
    await screen.findByLabelText('Review route')

    fireEvent.click(screen.getByLabelText('Plan route'))
    fireEvent.click(await screen.findByText('careful'))
    fireEvent.click(screen.getByRole('button', { name: 'Create mission' }))

    await waitFor(() =>
      expect(createMission).toHaveBeenCalledWith(expect.objectContaining({ plan_route: 'careful' })),
    )
  })

  it('omits plan_route from the create payload when left on the default', async () => {
    vi.mocked(createMission).mockResolvedValue({ id: 'm14' } as Mission)
    renderForm(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'Fix a bug' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create mission' }))

    await waitFor(() =>
      expect(createMission).toHaveBeenCalledWith(expect.objectContaining({ plan_route: undefined })),
    )
  })

  it('hydrates plan_route from the schedule template in edit mode', async () => {
    const scheduleWithPlanRoute: Schedule = {
      ...schedule,
      mission_template: { ...schedule.mission_template, plan_route: 'careful' },
    }
    renderForm(
      <MissionForm mode="edit" schedule={scheduleWithPlanRoute} onDone={vi.fn()} onCancel={vi.fn()} />,
    )

    await screen.findByDisplayValue('weekly-digest')
    expect(screen.getByLabelText('Plan route')).toHaveTextContent('careful')
  })

  it('submits the hydrated plan_route unchanged on save', async () => {
    vi.mocked(patchSchedule).mockResolvedValue(schedule)
    const scheduleWithPlanRoute: Schedule = {
      ...schedule,
      mission_template: { ...schedule.mission_template, plan_route: 'careful' },
    }
    renderForm(
      <MissionForm mode="edit" schedule={scheduleWithPlanRoute} onDone={vi.fn()} onCancel={vi.fn()} />,
    )

    await screen.findByDisplayValue('weekly-digest')
    fireEvent.click(screen.getByRole('button', { name: 'Save schedule' }))

    await waitFor(() =>
      expect(patchSchedule).toHaveBeenCalledWith(
        's1',
        expect.objectContaining({
          mission_template: expect.objectContaining({ plan_route: 'careful' }),
        }),
      ),
    )
  })
})

describe('MissionForm — edit mode', () => {
  it('prefills from the schedule, chip locked to the template kind', async () => {
    renderForm(<MissionForm mode="edit" schedule={schedule} onDone={vi.fn()} onCancel={vi.fn()} />)

    expect(await screen.findByDisplayValue('weekly-digest')).toBeTruthy()
    expect(screen.getByDisplayValue('Summarize the week')).toBeTruthy()
    expect(screen.getByText('Weekdays, 8:00 AM')).toBeTruthy()
    expect(screen.getByLabelText('Expires')).toHaveTextContent('Aug 1, 2026, 12:30')
    expect(screen.getByText('General · scratch workspace')).toBeInTheDocument()
    expect(classifyMission).not.toHaveBeenCalled()
  })

  it('auto-expands Advanced when the schedule has a non-default review route', async () => {
    renderForm(<MissionForm mode="edit" schedule={schedule} onDone={vi.fn()} onCancel={vi.fn()} />)

    await screen.findByDisplayValue('weekly-digest')
    expect(screen.getByLabelText('Review route')).toBeInTheDocument()
  })

  it('preserves the schedule kind in the patch payload and never shows Run once', async () => {
    vi.mocked(patchSchedule).mockResolvedValue(schedule)
    const onDone = vi.fn()
    renderForm(<MissionForm mode="edit" schedule={schedule} onDone={onDone} onCancel={vi.fn()} />)

    expect(screen.queryByRole('button', { name: 'Run once' })).toBeNull()

    await screen.findByDisplayValue('weekly-digest')
    fireEvent.click(screen.getByRole('button', { name: 'Save schedule' }))

    await waitFor(() =>
      expect(patchSchedule).toHaveBeenCalledWith(
        's1',
        expect.objectContaining({
          name: 'weekly-digest',
          mission_template: expect.objectContaining({ kind: 'general' }),
        }),
      ),
    )
    expect(onDone).toHaveBeenCalledWith({ kind: 'schedule', id: 's1' })
  })

  it('picks a new expiry date from the calendar and submits it', async () => {
    vi.mocked(patchSchedule).mockResolvedValue(schedule)
    renderForm(<MissionForm mode="edit" schedule={schedule} onDone={vi.fn()} onCancel={vi.fn()} />)

    await screen.findByDisplayValue('weekly-digest')
    fireEvent.click(screen.getByLabelText('Expires'))
    fireEvent.click(await screen.findByRole('button', { name: /August 15th, 2026/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Save schedule' }))

    await waitFor(() =>
      expect(patchSchedule).toHaveBeenCalledWith(
        's1',
        expect.objectContaining({
          expires_at: expect.stringContaining('-15T12:30'),
        }),
      ),
    )
  })

  it('clears the expiry back to never', async () => {
    vi.mocked(patchSchedule).mockResolvedValue(schedule)
    renderForm(<MissionForm mode="edit" schedule={schedule} onDone={vi.fn()} onCancel={vi.fn()} />)

    await screen.findByDisplayValue('weekly-digest')
    fireEvent.click(screen.getByLabelText('Expires'))
    fireEvent.click(await screen.findByRole('button', { name: 'Clear' }))
    expect(screen.getByLabelText('Expires')).toHaveTextContent('Never')

    fireEvent.click(screen.getByRole('button', { name: 'Save schedule' }))
    await waitFor(() =>
      expect(patchSchedule).toHaveBeenCalledWith(
        's1',
        expect.objectContaining({ expires_at: null }),
      ),
    )
  })
})
