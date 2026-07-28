import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Mission, MissionEvent } from '../api/types'
import { MissionDetail } from './MissionDetail'

vi.mock('../lib/alertSound', () => ({ playAlertSound: vi.fn() }))

vi.mock('../api/client', () => ({
  getMission: vi.fn(),
  missionEvents: vi.fn(),
  missionUsage: vi.fn(),
  resumeMission: vi.fn(),
  cancelMission: vi.fn(),
  answerMissionPermission: vi.fn(),
  listMissionFiles: vi.fn(),
  listSchedules: vi.fn(),
  pushMission: vi.fn(),
  downloadMissionFile: vi.fn(),
  downloadMissionArchive: vi.fn(),
  secretStatus: vi.fn(),
  listConnectors: vi.fn(),
}))

import {
  answerMissionPermission,
  cancelMission,
  getMission,
  listConnectors,
  listMissionFiles,
  listSchedules,
  missionEvents,
  missionUsage,
  resumeMission,
  secretStatus,
} from '../api/client'
import { playAlertSound } from '../lib/alertSound'

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
      </Routes>
    </MemoryRouter>,
  )
}

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(getMission).mockResolvedValue(baseMission)
  vi.mocked(missionEvents).mockResolvedValue(events)
  vi.mocked(missionUsage).mockResolvedValue({
    mission_id: 'm1',
    cost_usd: 0,
    input_tokens: 0,
    output_tokens: 0,
    requests: 0,
    unpriced_requests: 0,
    models: [],
  })
  vi.mocked(listMissionFiles).mockResolvedValue({ files: [], truncated: false })
  vi.mocked(listSchedules).mockResolvedValue([])
  vi.mocked(secretStatus).mockResolvedValue({ configured: false, backend: '' })
  vi.mocked(listConnectors).mockResolvedValue([])
})

describe('MissionDetail spend', () => {
  it('shows cost, calls, tokens, and budget share once usage exists', async () => {
    vi.mocked(getMission).mockResolvedValue({ ...baseMission, budget_usd: 2 })
    vi.mocked(missionUsage).mockResolvedValue({
      mission_id: 'm1',
      cost_usd: 0.5,
      input_tokens: 120_000,
      output_tokens: 8_000,
      requests: 7,
      unpriced_requests: 2,
      models: [{ provider: 'GLM (Z.ai)', model: 'glm-5.2', requests: 7, last_used: '2026-01-01T00:00:00Z' }],
    })
    renderPage()
    expect(await screen.findByText('Spend')).toBeTruthy()
    expect(screen.getByText('$0.5000')).toBeTruthy()
    expect(screen.getByText('7 model calls')).toBeTruthy()
    expect(screen.getByText('120,000 in / 8,000 out')).toBeTruthy()
    expect(screen.getByText('25% of budget')).toBeTruthy()
    expect(screen.getByText('glm-5.2')).toBeTruthy()
    expect(screen.getByText('2 unpriced calls')).toBeTruthy()
  })

  it('hides the section while the mission has no ledger rows', async () => {
    renderPage()
    await screen.findByText('Fix the login bug')
    expect(screen.queryByText('Spend')).toBeNull()
  })
})

describe('MissionDetail', () => {
  it('renders mission header, plan, and progress', async () => {
    renderPage()
    expect(await screen.findByText('Fix the login bug')).toBeTruthy()
    expect(screen.getByText('Add validation')).toBeTruthy()
    expect(screen.getByText('found the root cause')).toBeTruthy()
  })

  it('does not show a permission banner when none is pending', async () => {
    renderPage()
    await screen.findByText('Fix the login bug')
    expect(screen.queryByText(/Allow/)).toBeNull()
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
    renderPage()
    expect(await screen.findByText('shell')).toBeTruthy()
    expect(screen.getByText('destructive')).toBeTruthy()
    expect(screen.getByText('deletes files')).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: 'Allow once' }))
    await waitFor(() => expect(answerMissionPermission).toHaveBeenCalledWith('m1', 'once'))
  })

  it('plays an alert sound only on the transition into a permission block, not on later polls', async () => {
    vi.useFakeTimers()
    try {
      vi.mocked(getMission).mockResolvedValue(baseMission)
      renderPage()
      await vi.waitFor(() => expect(getMission).toHaveBeenCalled())
      expect(playAlertSound).not.toHaveBeenCalled()

      vi.mocked(getMission).mockResolvedValue({
        ...baseMission,
        pending_permission: 'perm-1',
        pending_permission_tool: 'shell',
      })
      await vi.advanceTimersByTimeAsync(5000)
      await vi.waitFor(() => expect(playAlertSound).toHaveBeenCalledTimes(1))

      // Still pending on the next poll — must not chime again.
      await vi.advanceTimersByTimeAsync(1500 * 2)
      expect(playAlertSound).toHaveBeenCalledTimes(1)
    } finally {
      vi.useRealTimers()
    }
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

  it('shows the push branch button for a coding mission with a branch', async () => {
    renderPage()
    await screen.findByText('Fix the login bug')
    expect(screen.getByRole('button', { name: 'Push branch' })).toBeTruthy()
  })

  it('hides the push branch button for a non-coding mission', async () => {
    vi.mocked(getMission).mockResolvedValue({ ...baseMission, kind: 'research' })
    renderPage()
    await screen.findByText('Fix the login bug')
    expect(screen.queryByRole('button', { name: 'Push branch' })).toBeNull()
  })

  it('hides the push branch button when the mission has no branch', async () => {
    vi.mocked(getMission).mockResolvedValue({ ...baseMission, branch: undefined })
    renderPage()
    await screen.findByText('Fix the login bug')
    expect(screen.queryByRole('button', { name: 'Push branch' })).toBeNull()
  })

  it('stops polling once the mission reaches a terminal phase with no pending permission', async () => {
    vi.useFakeTimers()
    try {
      vi.mocked(getMission).mockResolvedValue({ ...baseMission, phase: 'done', status: 'done' })
      renderPage()
      // One call on initial mount, one more when the effect re-runs after
      // `mission` resolves (phase dep flips from undefined to 'done') —
      // then the terminal branch stops scheduling any further interval.
      await vi.waitFor(() => expect(getMission).toHaveBeenCalledTimes(2))
      const callsAtTerminal = vi.mocked(getMission).mock.calls.length

      await vi.advanceTimersByTimeAsync(5000 * 3)

      expect(getMission).toHaveBeenCalledTimes(callsAtTerminal)
    } finally {
      vi.useRealTimers()
    }
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

  it('shows a recurring schedule strip when the mission fired from a schedule', async () => {
    vi.mocked(getMission).mockResolvedValue({ ...baseMission, schedule_id: 'sc1' })
    vi.mocked(listSchedules).mockResolvedValue([
      {
        id: 'sc1',
        name: 'daily-brief',
        cron: '0 7 * * *',
        mission_template: { goal: 'brief', kind: 'research' },
        enabled: true,
        next_run: '2026-01-02T07:00:00Z',
        created_at: '2026-01-01T00:00:00Z',
        updated_at: '2026-01-01T00:00:00Z',
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
