import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Mission, MissionEvent } from '../api/types'
import type { Signal } from '../lib/events'
import { MissionDetail } from './MissionDetail'

vi.mock('../lib/alertSound', () => ({ playAlertSound: vi.fn() }))
vi.mock('../lib/events', () => ({ subscribeEvents: vi.fn() }))

vi.mock('../api/client', () => ({
  getMission: vi.fn(),
  missionEvents: vi.fn(),
  missionUsage: vi.fn(),
  resumeMission: vi.fn(),
  sendMissionNote: vi.fn(),
  cancelMission: vi.fn(),
  deleteMission: vi.fn(),
  answerMissionPermission: vi.fn(),
  listMissionFiles: vi.fn(),
  listSchedules: vi.fn(),
  downloadMissionFile: vi.fn(),
  downloadMissionArchive: vi.fn(),
  pushMission: vi.fn(),
  openMissionPR: vi.fn(),
  fetchAttachmentBlob: vi.fn(),
}))

import {
  answerMissionPermission,
  cancelMission,
  deleteMission,
  getMission,
  listMissionFiles,
  listSchedules,
  missionEvents,
  missionUsage,
  openMissionPR,
  pushMission,
  resumeMission,
  sendMissionNote,
} from '../api/client'
import { playAlertSound } from '../lib/alertSound'
import { subscribeEvents } from '../lib/events'

// captureSubscribe grabs the onSignal/onReady callbacks subscribeEvents
// was last called with, so a test can fire them directly instead of
// waiting on a real SSE stream.
function captureSubscribe() {
  const unsubscribe = vi.fn()
  vi.mocked(subscribeEvents).mockReturnValue(unsubscribe)
  return {
    fireSignal: (sig: Signal) => vi.mocked(subscribeEvents).mock.calls.at(-1)?.[0](sig),
    fireReady: () => vi.mocked(subscribeEvents).mock.calls.at(-1)?.[1]?.(),
    unsubscribe,
  }
}

const baseMission: Mission = {
  id: 'm1',
  goal: 'Fix the login bug',
  kind: 'coding',
  phase: 'execute',
  status: 'working',
  branch: 'mission/fix-login',
  base_commit: 'abc123def456',
  workspace: 'ws-1',
  spec: { units: [{ title: 'Add validation', verify_cmd: 'go test', passes: true }] },
  progress: [{ at: '2026-01-01T00:00:00Z', note: 'found the root cause' }],
  iteration: 2,
  max_iterations: 8,
  consecutive_failures: 0,
  stall_count: 0,
  route: 'default',
  review_route: 'default',
  auto_approve_safe: true,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
}

const events: MissionEvent[] = [
  {
    mission_id: 'm1',
    seq: 1,
    kind: 'mission.created',
    payload: {},
    provenance: 'live',
    created_at: '2026-01-01T00:00:00Z',
  },
  {
    mission_id: 'm1',
    seq: 2,
    kind: 'mission.some_future_kind',
    payload: { detail: 'x' },
    provenance: 'live',
    created_at: '2026-01-01T00:01:00Z',
  },
  {
    mission_id: 'm1',
    seq: 3,
    kind: 'mission.permission_requested',
    payload: { tool: 'shell', args: '{"command":"rm -rf /tmp/x"}', danger: 'destructive' },
    provenance: 'live',
    created_at: '2026-01-01T00:02:00Z',
  },
  {
    mission_id: 'm1',
    seq: 4,
    kind: 'mission.permission_answered',
    payload: { tool: 'shell', decision: 'once' },
    provenance: 'live',
    created_at: '2026-01-01T00:03:00Z',
  },
]

function renderPage(id = 'm1') {
  return render(
    <MemoryRouter initialEntries={[`/missions/${id}`]}>
      <Routes>
        <Route path="/missions/:id" element={<MissionDetail />} />
        <Route path="/missions" element={<div>Missions list</div>} />
      </Routes>
    </MemoryRouter>,
  )
}

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(subscribeEvents).mockReturnValue(vi.fn())
  vi.mocked(getMission).mockResolvedValue(baseMission)
  vi.mocked(missionEvents).mockResolvedValue(events)
  vi.mocked(missionUsage).mockResolvedValue({
    mission_id: 'm1',
    cost_by_currency: {},
    input_tokens: 0,
    output_tokens: 0,
    requests: 0,
    unpriced_requests: 0,
    models: [],
  })
  vi.mocked(listMissionFiles).mockResolvedValue({ files: [], truncated: false })
  vi.mocked(listSchedules).mockResolvedValue([])
})

describe('MissionDetail spend', () => {
  it('shows cost, calls, tokens, and budget share once usage exists', async () => {
    vi.mocked(getMission).mockResolvedValue({ ...baseMission, budget_amount: 2, budget_currency: 'USD' })
    vi.mocked(missionUsage).mockResolvedValue({
      mission_id: 'm1',
      cost_by_currency: { USD: 0.5 },
      input_tokens: 120_000,
      output_tokens: 8_000,
      requests: 7,
      unpriced_requests: 2,
      models: [
        { provider: 'GLM (Z.ai)', model: 'glm-5.2', harness: false, requests: 7, last_used: '2026-01-01T00:00:00Z' },
      ],
    })
    renderPage()
    expect(await screen.findByText('$0.5000')).toBeTruthy()
    expect(screen.getByText('7 calls')).toBeTruthy()
    expect(screen.getByText('120.0k→8.0k tok')).toBeTruthy()
    expect(screen.getByText('25% of budget')).toBeTruthy()
    expect(screen.getByText('glm-5.2')).toBeTruthy()
    expect(screen.getByText('2 unpriced calls')).toBeTruthy()
    expect(screen.getByText('glm-5.2').closest('span')?.querySelector('svg use')).toHaveAttribute(
      'href',
      '#plogo-zai',
    )
  })

  it('omits the provider brand mark for an unrecognized provider name', async () => {
    vi.mocked(missionUsage).mockResolvedValue({
      mission_id: 'm1',
      cost_by_currency: { USD: 0.5 },
      input_tokens: 120_000,
      output_tokens: 8_000,
      requests: 7,
      unpriced_requests: 0,
      models: [
        {
          provider: 'my-custom-endpoint',
          model: 'whatever',
          harness: false,
          requests: 7,
          last_used: '2026-01-01T00:00:00Z',
        },
      ],
    })
    renderPage()
    expect(await screen.findByText('whatever')).toBeTruthy()
    expect(screen.getByText('whatever').closest('span')?.querySelector('svg use')).not.toBeInTheDocument()
  })

  it('filters harness-flagged models out of the brain pill row', async () => {
    vi.mocked(missionUsage).mockResolvedValue({
      mission_id: 'm1',
      cost_by_currency: { USD: 0.5 },
      input_tokens: 120_000,
      output_tokens: 8_000,
      requests: 7,
      unpriced_requests: 0,
      models: [
        { provider: 'GLM (Z.ai)', model: 'glm-5.2', harness: false, requests: 5, last_used: '2026-01-01T00:00:00Z' },
        { provider: 'Anthropic', model: 'sonnet', harness: true, requests: 2, last_used: '2026-01-01T00:00:00Z' },
      ],
    })
    renderPage()
    expect(await screen.findByText('glm-5.2')).toBeTruthy()
    expect(screen.queryByText('sonnet')).toBeNull()
  })

  it('hides the cost pills while the mission has no ledger rows', async () => {
    renderPage()
    await screen.findByText('Fix the login bug')
    expect(screen.queryByText(/calls$/)).toBeNull()
  })

  it('shows the converted total with no tooltip when there is no unbilled cost', async () => {
    vi.mocked(missionUsage).mockResolvedValue({
      mission_id: 'm1',
      cost_by_currency: { USD: 8 },
      converted_cost_by_currency: { EUR: 6.88 },
      rate_as_of: '2026-07-20',
      input_tokens: 100,
      output_tokens: 50,
      requests: 3,
      unpriced_requests: 0,
      models: [],
    })
    renderPage()
    const pill = await screen.findByText('€6.88')
    fireEvent.focus(pill)
    // No unbilled cost: the pill has no Tooltip wrapper at all, so
    // nothing extra renders on focus (in particular, never the billed
    // USD amount or a brain/harness breakdown — both dropped from the
    // tooltip).
    expect(screen.queryByText(/\$|unbilled/)).toBeNull()
  })

  it('omits the tooltip entirely when the mission has billed cost but no unbilled cost', async () => {
    vi.mocked(missionUsage).mockResolvedValue({
      mission_id: 'm1',
      cost_by_currency: { USD: 1.11 },
      billed_brain_by_currency: { USD: 1.11 },
      billed_harness_by_currency: {},
      input_tokens: 100,
      output_tokens: 50,
      requests: 3,
      unpriced_requests: 0,
      models: [],
    })
    renderPage()
    const pill = await screen.findByText('$1.11')
    fireEvent.focus(pill)
    expect(screen.queryByText(/unbilled/)).toBeNull()
  })

  it('shows only the unbilled line, in the pill\'s own currency, when a converted unbilled amount exists', async () => {
    vi.mocked(missionUsage).mockResolvedValue({
      mission_id: 'm1',
      cost_by_currency: { USD: 0.32 },
      converted_cost_by_currency: { BDT: 32.1 },
      billed_brain_by_currency: { USD: 0.11 },
      billed_harness_by_currency: { USD: 0.21 },
      unbilled_cost_by_currency: { USD: 0.94 },
      converted_unbilled_cost_by_currency: { BDT: 94.25 },
      input_tokens: 100,
      output_tokens: 50,
      requests: 3,
      unpriced_requests: 0,
      models: [],
    })
    renderPage()
    // The pill displays the converted (BDT) amount; its tooltip must
    // show the unbilled line in that SAME currency (BDT), never the
    // dropped brain/harness breakdown or a different currency's figure.
    const pill = await screen.findByText('৳32.10')
    fireEvent.focus(pill)
    expect(await screen.findByText('+৳94.25 unbilled')).toBeTruthy()
    expect(screen.queryByText(/brain|harness/)).toBeNull()
  })

  it('shows the unbilled line in the raw billed currency when no converted unbilled amount exists', async () => {
    vi.mocked(missionUsage).mockResolvedValue({
      mission_id: 'm1',
      cost_by_currency: { USD: 0 },
      unbilled_cost_by_currency: { USD: 0.5 },
      input_tokens: 100,
      output_tokens: 50,
      requests: 3,
      unpriced_requests: 0,
      models: [],
    })
    renderPage()
    const pill = await screen.findByText('$0')
    fireEvent.focus(pill)
    expect(await screen.findByText('+$0.5000 unbilled')).toBeTruthy()
  })
})

describe('MissionDetail retries/turns/processing/elapsed', () => {
  it('omits Retries when iteration is zero', async () => {
    vi.mocked(getMission).mockResolvedValue({ ...baseMission, iteration: 0 })
    renderPage()
    await screen.findByText('Fix the login bug')
    expect(screen.queryByText(/Retries/)).toBeNull()
  })

  it('shows Retries N when iteration is greater than zero', async () => {
    vi.mocked(getMission).mockResolvedValue({ ...baseMission, iteration: 3 })
    renderPage()
    expect(await screen.findByText('Retries 3')).toBeTruthy()
  })

  it('counts turns and sums processing time from mission.turn events', async () => {
    vi.mocked(missionEvents).mockResolvedValue([
      ...events,
      {
        mission_id: 'm1',
        seq: 5,
        kind: 'mission.turn',
        payload: { phase: 'execute', duration_ms: 30_000, ok: true },
        provenance: 'live',
        created_at: '2026-01-01T00:04:00Z',
      },
      {
        mission_id: 'm1',
        seq: 6,
        kind: 'mission.turn',
        payload: { phase: 'execute', duration_ms: 17_500, ok: true },
        provenance: 'live',
        created_at: '2026-01-01T00:05:00Z',
      },
    ])
    renderPage()
    expect(await screen.findByText('2 turns')).toBeTruthy()
    expect(screen.getByText('proc 47.5s')).toBeTruthy()
  })

  it('uses singular "turn" for exactly one mission.turn event', async () => {
    vi.mocked(missionEvents).mockResolvedValue([
      ...events,
      {
        mission_id: 'm1',
        seq: 5,
        kind: 'mission.turn',
        payload: { phase: 'execute', duration_ms: 1000, ok: true },
        provenance: 'live',
        created_at: '2026-01-01T00:04:00Z',
      },
    ])
    renderPage()
    expect(await screen.findByText('1 turn')).toBeTruthy()
  })

  it('computes Elapsed from created_at to updated_at for a terminal mission', async () => {
    vi.mocked(getMission).mockResolvedValue({
      ...baseMission,
      phase: 'done',
      status: 'done',
      created_at: '2026-01-01T00:00:00Z',
      updated_at: '2026-01-01T00:01:21Z',
    })
    renderPage()
    expect(await screen.findByText('total 1m 21s')).toBeTruthy()
  })
})

describe('MissionDetail harness pill', () => {
  it('shows just the model text (the icon alone identifies the harness), with the harness name in the accessible name/tooltip', async () => {
    vi.mocked(missionEvents).mockResolvedValue([
      ...events,
      {
        mission_id: 'm1',
        seq: 5,
        kind: 'executor.spawned',
        payload: { harness: 'claude-cli', provider: 'Anthropic', model: 'sonnet', auth_mode: 'api_key' },
        provenance: 'live',
        created_at: '2026-01-01T00:04:00Z',
      },
    ])
    renderPage()
    const pill = await screen.findByText('sonnet')
    expect(screen.queryByText(/Claude Code · sonnet/)).toBeNull()
    expect(pill.closest('span')).toHaveAttribute('title', expect.stringContaining('Anthropic'))
    expect(pill.closest('span')).toHaveAttribute('title', expect.stringContaining('Claude Code'))
    expect(pill.closest('span')).toHaveAttribute('aria-label', 'Claude Code harness')
  })

  it('omits the harness pill when no executor.spawned event exists', async () => {
    renderPage()
    await screen.findByText('Fix the login bug')
    expect(screen.queryByText(/Claude Code/)).toBeNull()
    expect(screen.queryByLabelText(/harness$/)).toBeNull()
  })
})

describe('MissionDetail environment pill', () => {
  it('shows an icon-only pill, with the env name as the accessible name/tooltip, when the environment has a known icon', async () => {
    vi.mocked(getMission).mockResolvedValue({ ...baseMission, environment: 'go' })
    renderPage()
    const pill = await screen.findByLabelText('go environment')
    expect(pill).toHaveAttribute('title', 'go environment')
    expect(pill.querySelector('svg')).toBeTruthy()
    expect(screen.queryByText('env · go')).toBeNull()
  })

  it('falls back to text for an environment with no icon (e.g. base), never rendering an empty pill', async () => {
    vi.mocked(getMission).mockResolvedValue({ ...baseMission, environment: 'base' })
    renderPage()
    expect(await screen.findByText('env · base')).toBeTruthy()
  })

  it('omits the environment pill when mission.environment is empty', async () => {
    renderPage()
    await screen.findByText('Fix the login bug')
    expect(screen.queryByText(/^env ·/)).toBeNull()
  })
})

describe('MissionDetail on_complete badge', () => {
  it('shows the auto-push badge when on_complete is push', async () => {
    vi.mocked(getMission).mockResolvedValue({ ...baseMission, on_complete: 'push' })
    renderPage()
    expect(await screen.findByText('auto-push')).toBeTruthy()
    expect(screen.queryByText('auto-PR')).toBeNull()
  })

  it('shows the auto-PR badge when on_complete is push_pr', async () => {
    vi.mocked(getMission).mockResolvedValue({ ...baseMission, on_complete: 'push_pr' })
    renderPage()
    expect(await screen.findByText('auto-PR')).toBeTruthy()
    expect(screen.queryByText('auto-push')).toBeNull()
  })

  it('omits both badges when on_complete is empty', async () => {
    renderPage()
    await screen.findByText('Fix the login bug')
    expect(screen.queryByText('auto-push')).toBeNull()
    expect(screen.queryByText('auto-PR')).toBeNull()
  })
})

describe('MissionDetail created timestamp', () => {
  it('shows the relative created time with an absolute tooltip', async () => {
    const createdAt = new Date(Date.now() - 3 * 3_600_000).toISOString()
    vi.mocked(getMission).mockResolvedValue({ ...baseMission, created_at: createdAt })
    renderPage()
    const el = await screen.findByText('created 3h ago')
    expect(el).toHaveAttribute('title', new Date(createdAt).toLocaleString())
  })
})

describe('MissionDetail', () => {
  it('renders mission header and plan, with no standalone progress section', async () => {
    renderPage()
    expect(await screen.findByText('Fix the login bug')).toBeTruthy()
    expect(screen.getByText('Add validation')).toBeTruthy()
    // Progress notes live in the Timeline now — the markdown-card
    // section that duplicated them is gone.
    expect(screen.queryByText('found the root cause')).toBeNull()
  })

  it('does not show a permission banner when none is pending', async () => {
    renderPage()
    await screen.findByText('Fix the login bug')
    expect(screen.queryByText(/Allow/)).toBeNull()
  })

  it('shows a linked GitHub origin beside the branch when repo_url is set', async () => {
    vi.mocked(getMission).mockResolvedValue({
      ...baseMission,
      repo_url: 'https://github.com/octocat/hello-world.git',
    })
    renderPage()
    await screen.findByText('Fix the login bug')

    const link = screen.getByRole('link', { name: 'octocat/hello-world' })
    expect(link).toHaveAttribute('href', 'https://github.com/octocat/hello-world')
    expect(link).toHaveAttribute('target', '_blank')
  })

  it('omits the GitHub origin link when repo_url is not set', async () => {
    renderPage()
    await screen.findByText('Fix the login bug')
    expect(screen.queryByRole('link', { name: /octocat/ })).toBeNull()
  })

  it('shows the generated name in the header instead of the goal when set', async () => {
    vi.mocked(getMission).mockResolvedValue({ ...baseMission, name: 'Fix Login Bug' })
    renderPage()
    const heading = await screen.findByRole('heading', { level: 1 })
    expect(heading.textContent).toBe('Fix Login Bug')
    expect(screen.queryByText('Fix the login bug')).toBeNull()
  })

  it('falls back to the goal in the header when name is empty', async () => {
    renderPage()
    const heading = await screen.findByRole('heading', { level: 1 })
    expect(heading.textContent).toBe('Fix the login bug')
  })

  it('shows a collapsed goal section under the header, rendering markdown once expanded', async () => {
    vi.mocked(getMission).mockResolvedValue({
      ...baseMission,
      name: 'Fix Login Bug',
      goal: 'Fix the **login** bug on staging',
    })
    renderPage()
    await screen.findByText('Fix Login Bug')

    expect(screen.getByText('Show goal')).toBeInTheDocument()
    expect(screen.queryByText('login')).toBeNull()

    fireEvent.click(screen.getByText('Show goal'))
    expect(screen.getByText('login').tagName).toBe('STRONG')
  })

  it('renders a plain-text goal in the goal section unchanged', async () => {
    renderPage()
    await screen.findByText('Fix the login bug')
    fireEvent.click(screen.getByText('Show goal'))
    expect(screen.getAllByText('Fix the login bug').length).toBeGreaterThan(0)
  })

  it('omits the Explore section when explore_notes is absent', async () => {
    renderPage()
    await screen.findByText('Fix the login bug')
    expect(screen.queryByText('Explore')).toBeNull()
  })

  it('shows a collapsed Explore section above Plan when explore_notes is set', async () => {
    vi.mocked(getMission).mockResolvedValue({
      ...baseMission,
      explore_notes: 'found **three** prior approaches',
    })
    renderPage()
    await screen.findByText('Fix the login bug')

    const headings = screen.getAllByRole('heading', { level: 2 }).map((h) => h.textContent)
    expect(headings.indexOf('Explore')).toBeGreaterThanOrEqual(0)
    expect(headings.indexOf('Explore')).toBeLessThan(headings.indexOf('Plan'))
    expect(screen.queryByText('three')).toBeNull()

    fireEvent.click(screen.getByText('Show exploration'))
    expect(screen.getByText('three').tagName).toBe('STRONG')
  })

  it('omits the Artifacts refs section when artifact_refs is absent', async () => {
    renderPage()
    await screen.findByText('Fix the login bug')
    expect(screen.queryByText('att-1')).toBeNull()
  })

  it('renders artifact ref chips when artifact_refs is set', async () => {
    vi.mocked(getMission).mockResolvedValue({
      ...baseMission,
      artifact_refs: [{ id: 'att-1', mime: 'text/markdown', name: 'report.md' }],
    })
    renderPage()
    await screen.findByText('Fix the login bug')

    expect(screen.getByText('report.md')).toBeInTheDocument()
  })

  it('shows the permission banner with tool detail when pending_permission is set', async () => {
    vi.mocked(getMission).mockResolvedValue({
      ...baseMission,
      pending_permission: 'perm-1',
      pending_permission_tool: 'shell',
      pending_permission_args: '{"command":"rm -rf /tmp/x"}',
      pending_permission_danger: 'destructive',
      pending_permission_rationale: 'deletes files',
    })
    // Fresh pending permission with no answered event of its own yet —
    // the shared `events` fixture's trailing permission_answered event
    // belongs to an earlier, unrelated cycle and must not be mistaken
    // for an answer to THIS one.
    vi.mocked(missionEvents).mockResolvedValue([])
    renderPage()
    expect(await screen.findByText('shell')).toBeTruthy()
    expect(screen.getByText('destructive')).toBeTruthy()
    expect(screen.getByText('deletes files')).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: 'Allow once' }))
    await waitFor(() => expect(answerMissionPermission).toHaveBeenCalledWith('m1', 'once'))
  })

  it('replaces the buttons with an answered status right after a click, and a new pending permission is actionable again', async () => {
    const sub = captureSubscribe()
    vi.mocked(getMission).mockResolvedValue({
      ...baseMission,
      pending_permission: 'perm-1',
      pending_permission_tool: 'shell',
      pending_permission_args: '{"command":"rm -rf /tmp/x"}',
      pending_permission_danger: 'destructive',
      pending_permission_rationale: 'deletes files',
    })
    vi.mocked(missionEvents).mockResolvedValue([])
    renderPage()
    expect(await screen.findByRole('button', { name: 'Allow once' })).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: 'Allow once' }))
    await waitFor(() => expect(answerMissionPermission).toHaveBeenCalledWith('m1', 'once'))
    expect(await screen.findByText('Approved — command running…')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Allow once' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Deny' })).toBeNull()

    // The refresh triggered by decidePermission still reports the SAME
    // pending_permission id (the tool call hasn't finished executing
    // yet) — the answered state must persist across it rather than
    // flashing back to actionable.
    await Promise.resolve()
    expect(screen.queryByRole('button', { name: 'Allow once' })).toBeNull()

    // A different pending_permission id arriving later (a fresh ask)
    // must render as a brand new actionable card — refetched via the
    // same signal path every other refresh in this page uses, not a
    // second mount.
    vi.mocked(getMission).mockResolvedValue({
      ...baseMission,
      pending_permission: 'perm-2',
      pending_permission_tool: 'gmail_send',
      pending_permission_rationale: 'sends an email',
    })
    sub.fireSignal({ kind: 'mission', id: 'm1' })
    expect(await screen.findByRole('button', { name: 'Allow once' })).toBeTruthy()
    expect(screen.queryByText('Approved — command running…')).toBeNull()
  })

  it('reverts to actionable and shows an error toast when the decision POST fails', async () => {
    vi.mocked(getMission).mockResolvedValue({
      ...baseMission,
      pending_permission: 'perm-1',
      pending_permission_tool: 'shell',
      pending_permission_rationale: 'deletes files',
    })
    vi.mocked(missionEvents).mockResolvedValue([])
    vi.mocked(answerMissionPermission).mockRejectedValue(new Error('not found'))
    renderPage()
    expect(await screen.findByRole('button', { name: 'Allow once' })).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: 'Allow once' }))
    await waitFor(() => expect(answerMissionPermission).toHaveBeenCalled())
    expect(await screen.findByRole('button', { name: 'Allow once' })).toBeTruthy()
    expect(screen.queryByText('Approved — command running…')).toBeNull()
  })

  it('treats a still-pending permission as answered when the events already show a later permission_answered', async () => {
    vi.mocked(getMission).mockResolvedValue({
      ...baseMission,
      pending_permission: 'perm-1',
      pending_permission_tool: 'shell',
      pending_permission_rationale: 'deletes files',
    })
    vi.mocked(missionEvents).mockResolvedValue([
      {
        mission_id: 'm1',
        seq: 1,
        kind: 'mission.permission_requested',
        payload: { tool: 'shell' },
        provenance: 'live',
        created_at: '2026-01-01T00:00:00Z',
      },
      {
        mission_id: 'm1',
        seq: 2,
        kind: 'mission.permission_answered',
        payload: { tool: 'shell', decision: 'once' },
        provenance: 'live',
        created_at: '2026-01-01T00:01:00Z',
      },
    ])
    renderPage()
    expect(await screen.findByText('Answered — waiting for the worker to continue…')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Allow once' })).toBeNull()
  })

  it('plays an alert sound only on the transition into a permission block, not on later refetches', async () => {
    const sub = captureSubscribe()
    vi.mocked(getMission).mockResolvedValue(baseMission)
    renderPage()
    await vi.waitFor(() => expect(getMission).toHaveBeenCalled())
    expect(playAlertSound).not.toHaveBeenCalled()

    vi.mocked(getMission).mockResolvedValue({
      ...baseMission,
      pending_permission: 'perm-1',
      pending_permission_tool: 'shell',
    })
    sub.fireSignal({ kind: 'mission', id: 'm1' })
    await vi.waitFor(() => expect(playAlertSound).toHaveBeenCalledTimes(1))

    // Still pending on the next refetch — must not chime again.
    sub.fireSignal({ kind: 'mission', id: 'm1' })
    await vi.waitFor(() => expect(getMission).toHaveBeenCalledTimes(3))
    expect(playAlertSound).toHaveBeenCalledTimes(1)
  })

  it('renders known event kinds with their specific text and unknown kinds with the fallback', async () => {
    renderPage()
    await screen.findByText('Fix the login bug')
    expect(await screen.findByText('Mission created')).toBeTruthy()
    // Unknown kind falls back to rendering the raw kind string.
    expect(screen.getByText('mission.some_future_kind')).toBeTruthy()
    expect(screen.getByText(/Permission requested: shell \(destructive\)/)).toBeTruthy()
    expect(screen.getByText('{"command":"rm -rf /tmp/x"}')).toBeTruthy()
    expect(screen.getByText('Permission once: shell')).toBeTruthy()
  })

  it('renders the timeline as a scrollable container with scroll-to-top/bottom controls', async () => {
    renderPage()
    await screen.findByText('Fix the login bug')
    expect(screen.getByTitle('Scroll to top')).toBeTruthy()
    expect(screen.getByTitle('Scroll to bottom')).toBeTruthy()
    expect(screen.getByText(`${events.length} events`)).toBeTruthy()
  })

  it('shows resume for a paused mission and cancel for a non-terminal one', async () => {
    vi.mocked(getMission).mockResolvedValue({ ...baseMission, status: 'paused', pause_reason: 'backoff' })
    renderPage()
    await screen.findByText('Fix the login bug')
    expect(screen.getByRole('button', { name: 'Resume' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Cancel' })).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: 'Resume' }))
    await waitFor(() => expect(resumeMission).toHaveBeenCalledWith('m1'))
  })

  it('sends a note for a working mission and clears the input on success', async () => {
    renderPage()
    await screen.findByText('Fix the login bug')
    const input = screen.getByPlaceholderText('Send a note to steer this mission…')
    fireEvent.change(input, { target: { value: 'focus on staging next' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send note' }))
    await waitFor(() => expect(sendMissionNote).toHaveBeenCalledWith('m1', 'focus on staging next'))
    await waitFor(() => expect(input).toHaveValue(''))
  })

  it('hides the note affordance once the mission is terminal', async () => {
    vi.mocked(getMission).mockResolvedValue({ ...baseMission, phase: 'done', status: 'idle' })
    renderPage()
    await screen.findByText('Fix the login bug')
    expect(screen.queryByPlaceholderText('Send a note to steer this mission…')).toBeNull()
  })

  it('surfaces the most recent mission.paused event detail while paused', async () => {
    vi.mocked(getMission).mockResolvedValue({ ...baseMission, status: 'paused', pause_reason: 'backoff' })
    vi.mocked(missionEvents).mockResolvedValue([
      ...events,
      {
        mission_id: 'm1',
        seq: 5,
        kind: 'mission.paused',
        payload: { reason: 'backoff', detail: 'every provider attempt failed: GLM 429' },
        provenance: 'live',
        created_at: '2026-01-01T00:04:00Z',
      },
    ])
    renderPage()
    expect(await screen.findByText('every provider attempt failed: GLM 429')).toBeTruthy()
  })

  it('omits the pause detail once the mission has been resumed', async () => {
    vi.mocked(getMission).mockResolvedValue({ ...baseMission, status: 'idle', pause_reason: '' })
    vi.mocked(missionEvents).mockResolvedValue([
      ...events,
      {
        mission_id: 'm1',
        seq: 5,
        kind: 'mission.paused',
        payload: { reason: 'backoff', detail: 'every provider attempt failed: GLM 429' },
        provenance: 'live',
        created_at: '2026-01-01T00:04:00Z',
      },
      {
        mission_id: 'm1',
        seq: 6,
        kind: 'mission.resumed',
        payload: {},
        provenance: 'live',
        created_at: '2026-01-01T00:05:00Z',
      },
    ])
    renderPage()
    await screen.findByText('Fix the login bug')
    expect(screen.queryByText('every provider attempt failed: GLM 429')).toBeNull()
  })

  it('hides resume and cancel for a done mission', async () => {
    vi.mocked(getMission).mockResolvedValue({ ...baseMission, phase: 'done', status: 'done' })
    renderPage()
    await screen.findByText('Fix the login bug')
    expect(screen.queryByRole('button', { name: 'Resume' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Cancel' })).toBeNull()
  })

  it('cancels a mission', async () => {
    renderPage()
    await screen.findByText('Fix the login bug')
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(cancelMission).toHaveBeenCalledWith('m1'))
  })

  it('hides delete for a non-terminal mission', async () => {
    renderPage()
    await screen.findByText('Fix the login bug')
    expect(screen.queryByRole('button', { name: 'Delete mission' })).toBeNull()
  })

  it('hides Fork for a non-terminal mission', async () => {
    renderPage()
    await screen.findByText('Fix the login bug')
    expect(screen.queryByRole('button', { name: 'Fork' })).toBeNull()
  })

  it('shows Fork for a done mission and navigates to a prefilled create form', async () => {
    vi.mocked(getMission).mockResolvedValue({ ...baseMission, phase: 'done', status: 'done' })
    render(
      <MemoryRouter initialEntries={['/missions/m1']}>
        <Routes>
          <Route path="/missions/:id" element={<MissionDetail />} />
          <Route path="/missions/new" element={<div>New mission page</div>} />
        </Routes>
      </MemoryRouter>,
    )
    await screen.findByText('Fix the login bug')
    fireEvent.click(screen.getByRole('button', { name: 'Fork' }))
    await screen.findByText('New mission page')
  })

  it('links to the parent mission when parent_mission_id is set', async () => {
    vi.mocked(getMission).mockResolvedValue({ ...baseMission, parent_mission_id: 'parent-123' })
    renderPage()
    await screen.findByText('Fix the login bug')
    expect(screen.getByRole('link', { name: /parent-1/ })).toHaveAttribute(
      'href',
      '/missions/parent-123',
    )
  })

  it('shows delete for a done mission and deletes on confirm', async () => {
    vi.mocked(getMission).mockResolvedValue({ ...baseMission, phase: 'done', status: 'done' })
    renderPage()
    await screen.findByText('Fix the login bug')
    fireEvent.click(screen.getByRole('button', { name: 'Delete mission' }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }))
    await waitFor(() => expect(deleteMission).toHaveBeenCalledWith('m1'))
    await screen.findByText('Missions list')
  })

  it('ignores a signal for a different mission id', async () => {
    const sub = captureSubscribe()
    renderPage()
    await screen.findByText('Fix the login bug')
    vi.mocked(getMission).mockClear()

    sub.fireSignal({ kind: 'mission', id: 'some-other-mission' })

    // Nothing to await on directly (a no-op produces no event), so
    // give any errant refetch a turn to happen, then assert it didn't.
    await Promise.resolve()
    expect(getMission).not.toHaveBeenCalled()
  })

  it('refetches on a signal naming this mission, and on ready', async () => {
    const sub = captureSubscribe()
    renderPage()
    await screen.findByText('Fix the login bug')
    vi.mocked(getMission).mockClear()

    sub.fireSignal({ kind: 'mission', id: 'm1' })
    await vi.waitFor(() => expect(getMission).toHaveBeenCalledTimes(1))

    sub.fireReady()
    await vi.waitFor(() => expect(getMission).toHaveBeenCalledTimes(2))
  })

  it('unsubscribes on unmount', async () => {
    const sub = captureSubscribe()
    const { unmount } = renderPage()
    await screen.findByText('Fix the login bug')
    unmount()
    expect(sub.unsubscribe).toHaveBeenCalled()
  })

  it('renders a Result section for a terminal mission with worker-reported evidence', async () => {
    vi.mocked(getMission).mockResolvedValue({
      ...baseMission,
      phase: 'done',
      status: 'done',
      last_evidence: 'some evidence text',
    })
    renderPage()
    expect(await screen.findByText('Result')).toBeTruthy()
    expect(screen.getByText('some evidence text')).toBeTruthy()
  })

  it('omits the Result section for a terminal mission with no evidence', async () => {
    vi.mocked(getMission).mockResolvedValue({ ...baseMission, phase: 'done', status: 'done' })
    renderPage()
    await screen.findByText('Fix the login bug')
    expect(screen.queryByText('Result')).toBeNull()
  })

  it('renders a light mission Result section from final_output, not last_evidence', async () => {
    vi.mocked(getMission).mockResolvedValue({
      ...baseMission,
      phase: 'done',
      status: 'done',
      light: true,
      last_evidence: 'worker evidence text',
      final_output: 'the complete deliverable',
    })
    renderPage()
    expect(await screen.findByText('Result')).toBeTruthy()
    expect(screen.getByText('the complete deliverable')).toBeTruthy()
    expect(screen.queryByText('worker evidence text')).toBeNull()
  })

  it('omits the Result section for a light mission with no final_output', async () => {
    vi.mocked(getMission).mockResolvedValue({
      ...baseMission,
      phase: 'done',
      status: 'done',
      light: true,
      last_evidence: 'worker evidence text',
    })
    renderPage()
    await screen.findByText('Fix the login bug')
    expect(screen.queryByText('Result')).toBeNull()
  })

  it('shows a recurring schedule strip when the mission fired from a schedule', async () => {
    vi.mocked(getMission).mockResolvedValue({ ...baseMission, schedule_id: 'sc1' })
    vi.mocked(listSchedules).mockResolvedValue([
      {
        id: 'sc1',
        name: 'daily-brief',
        cron: '0 7 * * *',
        mission_template: { goal: 'brief', kind: 'general' },
        enabled: true,
        next_run: '2026-01-02T07:00:00Z',
        created_at: '2026-01-01T00:00:00Z',
        updated_at: '2026-01-01T00:00:00Z',
        pending_fire: false,
      },
    ])
    renderPage()
    expect(await screen.findByText(/Recurring · Daily, 7:00 AM · next run/)).toBeTruthy()
  })

  it('omits the recurring strip for a one-off mission', async () => {
    renderPage()
    await screen.findByText('Fix the login bug')
    expect(screen.queryByText(/Recurring ·/)).toBeNull()
  })
})

describe('MissionDetail push/PR (github-connection missions)', () => {
  it('shows Push branch and Push & open PR only for a mission with connector_id', async () => {
    vi.mocked(getMission).mockResolvedValue({ ...baseMission, connector_id: 'conn-1' })
    renderPage()
    await screen.findByText('Fix the login bug')
    expect(screen.getByRole('button', { name: 'Push branch' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Push & open PR' })).toBeTruthy()
  })

  it('omits the push/PR buttons for a mission without connector_id', async () => {
    renderPage()
    await screen.findByText('Fix the login bug')
    expect(screen.queryByRole('button', { name: 'Push branch' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Push & open PR' })).toBeNull()
  })

  it('pushes the branch on click', async () => {
    vi.mocked(getMission).mockResolvedValue({ ...baseMission, connector_id: 'conn-1' })
    vi.mocked(pushMission).mockResolvedValue({ branch: 'mission/fix-login', remote_host: 'github.com' })
    renderPage()
    await screen.findByText('Fix the login bug')

    fireEvent.click(screen.getByRole('button', { name: 'Push branch' }))
    await waitFor(() => expect(pushMission).toHaveBeenCalledWith('m1'))
  })

  it('opens a PR on click and shows the resulting chip immediately', async () => {
    vi.mocked(getMission).mockResolvedValue({
      ...baseMission,
      connector_id: 'conn-1',
      repo_url: 'https://github.com/octocat/hello-world.git',
    })
    vi.mocked(openMissionPR).mockResolvedValue({
      url: 'https://github.com/octocat/hello-world/pull/9',
      number: 9,
    })
    renderPage()
    await screen.findByText('Fix the login bug')

    fireEvent.click(screen.getByRole('button', { name: 'Push & open PR' }))
    await waitFor(() => expect(openMissionPR).toHaveBeenCalledWith('m1'))

    const link = await screen.findByRole('link', { name: 'PR #9' })
    expect(link).toHaveAttribute('href', 'https://github.com/octocat/hello-world/pull/9')
  })

  it('renders the PR chip from a prior mission.pr_opened event on load, with no click needed', async () => {
    vi.mocked(getMission).mockResolvedValue({ ...baseMission, connector_id: 'conn-1' })
    vi.mocked(missionEvents).mockResolvedValue([
      ...events,
      {
        mission_id: 'm1',
        seq: 5,
        kind: 'mission.pr_opened',
        payload: { url: 'https://github.com/octocat/hello-world/pull/12', number: 12 },
        provenance: 'live',
        created_at: '2026-01-01T00:04:00Z',
      },
    ])
    renderPage()
    const link = await screen.findByRole('link', { name: 'PR #12' })
    expect(link).toHaveAttribute('href', 'https://github.com/octocat/hello-world/pull/12')
  })

  it('shows an error toast when the push fails', async () => {
    vi.mocked(getMission).mockResolvedValue({ ...baseMission, connector_id: 'conn-1' })
    vi.mocked(pushMission).mockRejectedValue(new Error('push failed'))
    renderPage()
    await screen.findByText('Fix the login bug')

    fireEvent.click(screen.getByRole('button', { name: 'Push branch' }))
    await waitFor(() => expect(pushMission).toHaveBeenCalled())
    expect(screen.getByRole('button', { name: 'Push branch' })).not.toBeDisabled()
  })

  it('hides Push & open PR once a PR exists, keeping the push button and the PR chip', async () => {
    vi.mocked(getMission).mockResolvedValue({ ...baseMission, connector_id: 'conn-1' })
    vi.mocked(missionEvents).mockResolvedValue([
      ...events,
      {
        mission_id: 'm1',
        seq: 5,
        kind: 'mission.pr_opened',
        payload: { url: 'https://github.com/octocat/hello-world/pull/12', number: 12 },
        provenance: 'live',
        created_at: '2026-01-01T00:04:00Z',
      },
    ])
    renderPage()
    await screen.findByRole('link', { name: 'PR #12' })

    expect(screen.getByRole('button', { name: 'Push branch' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Push & open PR' })).toBeNull()
  })
})
