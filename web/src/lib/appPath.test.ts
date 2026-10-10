import { describe, expect, it } from 'vitest'
import { isAppPath } from './appPath'

describe('isAppPath', () => {
  it.each([
    ['/', true],
    ['/analytics', true],
    ['/analytics/', true],
    ['/missions/new', true],
    ['/missions/abc123', true],
    ['/missions/abc/extra', false],
    ['/missions/schedules/s1/edit', true],
    ['/chat', true],
    ['/chat/abc', true],
    ['/chat/abc/def', false],
    ['/settings', true],
    ['/settings/features', true],
    ['/settings/agents/new', true],
    ['/knowledge', true],
    ['/knowledge/a/b', true],
    ['/welcome', true],
    ['/settings/features?tab=x#top', true],
    ['/nope', false],
    ['//evil.example/settings', false],
    ['//settings', false],
    ['http://evil.example/settings', false],
    ['https://timothy-agent.github.io/docs/', false],
    ['javascript:alert(1)', false],
    ['data:text/html,x', false],
    ['settings', false],
    ['/\\evil', false],
    ['', false],
  ])('%s -> %s', (href, want) => {
    expect(isAppPath(href)).toBe(want)
  })
})
