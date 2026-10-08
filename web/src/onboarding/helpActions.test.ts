import { describe, expect, it } from 'vitest'
import { tourPageFor } from './helpActions'

describe('tourPageFor', () => {
  it.each([
    ['/', null],
    ['/chat', 'chat'],
    ['/chat/abc', 'chat'],
    ['/chatter', null],
    ['/missions', 'missions'],
    ['/missions/', 'missions'],
    ['/missions/new', 'mission_new'],
    ['/missions/abc', null],
    ['/automations', 'automations'],
    ['/automations/new', null],
    ['/knowledge', 'knowledge'],
    ['/knowledge/abc', null],
    ['/memory', 'memory'],
    ['/analytics', 'analytics'],
    ['/settings/providers', 'settings'],
    ['/settings/agents/new', 'settings'],
    ['/welcome', null],
    ['/research', null],
  ])('%s maps to %s', (path, page) => {
    expect(tourPageFor(path)).toBe(page)
  })
})
