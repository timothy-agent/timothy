import axe from 'axe-core'
import { render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { Mission, MissionEvent } from '../api/types'
import { TooltipProvider } from '../components/ui/tooltip'
import { MissionDetail } from './MissionDetail'

vi.mock('../lib/alertSound', () => ({ playAlertSound: vi.fn() }))
vi.mock('../lib/events', () => ({ subscribeEvents: vi.fn(() => vi.fn()) }))

vi.mock('../api/client', () => ({
  getMission: vi.fn(),
  missionEvents: vi.fn(),
  missionUsage: vi.fn(),
  resumeMission: vi.fn(),
  sendMissionNote: vi.fn(),
  cancelMission: vi.fn(),
  deleteMission: vi.fn(),
  answerMissionPermission: vi.fn(),
  answerMissionQuestion: vi.fn(),
  approveMissionPlan: vi.fn(),
  replanMission: vi.fn(),
  rediscoverMission: vi.fn(),
  listMissionFiles: vi.fn(),
  listSchedules: vi.fn(),
  downloadMissionFile: vi.fn(),
  downloadMissionArchive: vi.fn(),
  downloadMissionPdfExport: vi.fn(),
  exportMissionPdf: vi.fn(),
  getSettings: vi.fn(),
  pushMission: vi.fn(),
  openMissionPR: vi.fn(),
  fetchAttachmentBlob: vi.fn(),
  getMissionExecutionPlan: vi.fn().mockResolvedValue([]),
  patchMissionRouting: vi.fn(),
  listRoutes: vi.fn().mockResolvedValue([]),
  listDestinations: vi.fn().mockResolvedValue([]),
}))

import { getMission, getSettings, listMissionFiles, listSchedules, missionEvents, missionUsage } from '../api/client'

const baseMission: Mission = {
  id: 'm1',
  goal: 'Fix the login bug',
  kind: 'coding',
  phase: 'generate',
  status: 'working',
  plan: { units: [] },
  progress: [],
  iteration: 2,
  max_iterations: 8,
  consecutive_failures: 0,
  stall_count: 0,
  route: 'default',
  review_route: 'default',
  auto_approve_tools: true,
  auto_approve_plan: true,
  asks_used: 0,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
}

const events: MissionEvent[] = []

function renderPage() {
  return render(
    <TooltipProvider>
      <MemoryRouter initialEntries={['/missions/m1']}>
        <Routes>
          <Route path="/missions/:id" element={<MissionDetail />} />
        </Routes>
      </MemoryRouter>
    </TooltipProvider>,
  )
}

beforeEach(() => {
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
  vi.mocked(listSchedules).mockResolvedValue([])
  vi.mocked(getSettings).mockResolvedValue({ settings: {}, values: {} })
  vi.mocked(listMissionFiles).mockResolvedValue({ files: [], truncated: false })
})

describe('MissionDetail page accessibility', () => {
  it('has no axe violations for a working mission', async () => {
    const { container } = renderPage()
    await screen.findByRole('heading', { name: 'Fix the login bug' })

    // color-contrast/region disabled per the existing timothy a11y test
    // convention.
    const results = await axe.run(container, {
      rules: { 'color-contrast': { enabled: false }, region: { enabled: false } },
    })
    expect(results.violations).toEqual([])
  })

  it('has no axe violations with a pending permission gate', async () => {
    vi.mocked(getMission).mockResolvedValue({
      ...baseMission,
      pending_permission: 'perm-1',
      pending_permission_tool: 'shell',
      pending_permission_args: '{"command":"rm -rf /tmp/x"}',
      pending_permission_danger: 'destructive',
      pending_permission_rationale: 'deletes files',
    })
    const { container } = renderPage()
    await screen.findByRole('region')

    // color-contrast/region disabled per the existing timothy a11y test
    // convention.
    const results = await axe.run(container, {
      rules: { 'color-contrast': { enabled: false }, region: { enabled: false } },
    })
    expect(results.violations).toEqual([])
  })
})
