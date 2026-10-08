import type { OnboardingProgress, Readiness } from '../api/types'

export type ChecklistItemKey =
  | 'provider'
  | 'first_chat'
  | 'first_mission'
  | 'connect'
  | 'knowledge'
  | 'memory'
  | 'automations'
  | 'analytics'

export interface ChecklistItem {
  key: ChecklistItemKey
  title: string
  why: string
  to: string
  done: boolean
}

// buildChecklist lists the setup steps in order. Pages with nothing to
// set up count as done once the operator has opened them.
export function buildChecklist(readiness: Readiness, progress: OnboardingProgress): ChecklistItem[] {
  const visited = progress.visited ?? []
  return [
    {
      key: 'provider',
      title: 'Add a model provider',
      why: 'Timothy needs a model to think with.',
      to: '/settings/providers',
      done: readiness.chat_route,
    },
    {
      key: 'first_chat',
      title: 'Send your first chat',
      why: 'Ask anything. Chats remember what matters.',
      to: '/chat',
      done: readiness.first_chat,
    },
    {
      key: 'first_mission',
      title: 'Run your first mission',
      why: 'Missions are longer tasks Timothy works on by itself and reports back.',
      to: '/missions/new',
      done: readiness.first_mission,
    },
    {
      key: 'connect',
      title: 'Connect an account or a chat channel',
      why: 'Mail, calendar, GitHub, Telegram: Timothy can read and act through them.',
      to: '/settings/connectors',
      done: readiness.connectors > 0 || readiness.channels > 0,
    },
    {
      key: 'knowledge',
      title: 'Give Timothy knowledge',
      why: 'Upload documents Timothy can search and quote.',
      to: '/knowledge',
      done: readiness.kb_collections > 0,
    },
    {
      key: 'memory',
      title: 'See what Timothy remembers',
      why: 'Review what it learned and correct it.',
      to: '/memory',
      done: visited.includes('memory'),
    },
    {
      key: 'automations',
      title: 'Automate something',
      why: 'Run a mission on a schedule or when something happens.',
      to: '/automations',
      done: visited.includes('automations'),
    },
    {
      key: 'analytics',
      title: 'Check costs',
      why: 'See what each chat and mission cost.',
      to: '/analytics',
      done: visited.includes('analytics'),
    },
  ]
}

export function checklistProgress(items: ChecklistItem[]): { done: number; total: number; percent: number } {
  const done = items.filter((i) => i.done).length
  const total = items.length
  return { done, total, percent: total === 0 ? 0 : Math.round((done / total) * 100) }
}

// canDismiss: the checklist stays until a model provider works.
export function canDismiss(items: ChecklistItem[]): boolean {
  return items.find((i) => i.key === 'provider')?.done ?? false
}
