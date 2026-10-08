import { describe, expect, it } from 'vitest'
import type { Readiness } from '../api/types'
import { buildChecklist, canDismiss, checklistProgress, type ChecklistItemKey } from './checklist'
import { readyReadiness } from './testing'

const empty: Readiness = {
  ...readyReadiness,
  chat_route: false,
  first_chat: false,
  first_mission: false,
  connectors: 0,
  channels: 0,
  kb_collections: 0,
}

function doneOf(key: ChecklistItemKey, readiness: Readiness, visited: string[] = []) {
  return buildChecklist(readiness, { visited }).find((i) => i.key === key)?.done
}

describe('buildChecklist', () => {
  it('lists the eight items in order with links', () => {
    const items = buildChecklist(empty, {})
    expect(items.map((i) => [i.key, i.to])).toEqual([
      ['provider', '/settings/providers'],
      ['first_chat', '/chat'],
      ['first_mission', '/missions/new'],
      ['connect', '/settings/connectors'],
      ['knowledge', '/knowledge'],
      ['memory', '/memory'],
      ['automations', '/automations'],
      ['analytics', '/analytics'],
    ])
    expect(items.every((i) => !i.done)).toBe(true)
  })

  it.each([
    ['provider', { chat_route: true }],
    ['first_chat', { first_chat: true }],
    ['first_mission', { first_mission: true }],
    ['connect', { connectors: 1 }],
    ['connect', { channels: 2 }],
    ['knowledge', { kb_collections: 1 }],
  ] as const)('%s is done when readiness says %o', (key, patch) => {
    expect(doneOf(key, empty)).toBe(false)
    expect(doneOf(key, { ...empty, ...patch })).toBe(true)
  })

  it.each(['memory', 'automations', 'analytics'] as const)('%s is done once visited', (key) => {
    expect(doneOf(key, empty, [])).toBe(false)
    expect(doneOf(key, empty, ['other'])).toBe(false)
    expect(doneOf(key, empty, [key])).toBe(true)
  })

  it('treats missing visited as none', () => {
    expect(buildChecklist(empty, {}).find((i) => i.key === 'memory')?.done).toBe(false)
  })
})

describe('checklistProgress', () => {
  it.each([
    [[], 0, 0],
    [['memory'], 1, 13],
    [['memory', 'automations', 'analytics'], 3, 38],
    [['memory', 'automations'], 2, 25],
  ])('visited %o gives %i done and %i percent', (visited, done, percent) => {
    expect(checklistProgress(buildChecklist(empty, { visited }))).toEqual({ done, total: 8, percent })
  })

  it('is 100 when everything is done', () => {
    const all: Readiness = { ...readyReadiness, connectors: 1, kb_collections: 1 }
    const items = buildChecklist(all, { visited: ['memory', 'automations', 'analytics'] })
    expect(checklistProgress(items)).toEqual({ done: 8, total: 8, percent: 100 })
  })

  it('is 0 for an empty list', () => {
    expect(checklistProgress([])).toEqual({ done: 0, total: 0, percent: 0 })
  })
})

describe('canDismiss', () => {
  it('needs a model provider first', () => {
    expect(canDismiss(buildChecklist(empty, {}))).toBe(false)
    expect(canDismiss(buildChecklist({ ...empty, chat_route: true }, {}))).toBe(true)
  })
})
