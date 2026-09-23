import type { Automation, AutomationNote, AutomationRun, AutomationTemplate, AutomationTrigger } from '../../api/types'

// Wire-shaped fixtures shared by the automation tests.

export const agentID = '00000000-0000-0000-0000-00000000a001'

export function makeTrigger(over: Partial<AutomationTrigger> = {}): AutomationTrigger {
  return {
    id: 't1',
    automation_id: 's1',
    kind: 'cron',
    config: { expr: '0 8 * * 1-5' },
    state: {},
    enabled: true,
    created_at: '2026-07-01T00:00:00Z',
    updated_at: '2026-07-01T00:00:00Z',
    ...over,
  }
}

export function makeAutomation(over: Partial<Automation> = {}): Automation {
  return {
    id: 's1',
    name: 'weekly-digest',
    description: '',
    agent_id: agentID,
    action: { kind: 'mission', mission: { goal: 'Summarize the week', kind: 'general', auto_approve_tools: true } },
    concurrency: 'skip',
    max_concurrent: 1,
    max_runs_per_hour: 6,
    continuity: true,
    notes_enabled: true,
    consecutive_failures: 0,
    enabled: true,
    created_at: '2026-07-01T00:00:00Z',
    updated_at: '2026-07-01T00:00:00Z',
    triggers: [makeTrigger()],
    stats: { runs_total: 0, succeeded_7d: 0, failed_7d: 0, next_run_at: '2026-07-27T08:00:00Z' },
    ...over,
  }
}

export function makeRun(over: Partial<AutomationRun> = {}): AutomationRun {
  return {
    id: 'r1',
    automation_id: 's1',
    trigger_id: 't1',
    dedup_key: 'cron:t1:2026-07-20T08:00:00Z',
    status: 'done',
    event: {},
    created_at: '2026-07-20T08:00:00Z',
    started_at: '2026-07-20T08:00:01Z',
    finished_at: '2026-07-20T08:02:31Z',
    ...over,
  }
}

export function makeNote(over: Partial<AutomationNote> = {}): AutomationNote {
  return { name: 'progress', content: 'Last seen PR: #42', updated_at: '2026-07-20T08:02:31Z', ...over }
}

export function makeTemplate(over: Partial<AutomationTemplate> = {}): AutomationTemplate {
  return {
    id: 'pr-review',
    name: 'Review new pull requests',
    description: 'Reads each new PR and posts a summary.',
    icon: 'git-branch',
    action: { kind: 'mission', mission: { goal: 'Review {{event.pr_url}}', kind: 'general' } },
    triggers: [{ kind: 'cron', config: { expr: '0 * * * *' } }],
    requires: [{ kind: 'destination', value: 'email' }],
    missing: [],
    ...over,
  }
}
