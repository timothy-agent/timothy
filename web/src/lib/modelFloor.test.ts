import { describe, expect, it } from 'vitest'
import { belowMissionFloor } from './modelFloor'

describe('belowMissionFloor', () => {
  const floor = ['qwen2.5:7b', 'nova']
  const cases: [string, readonly string[] | null | undefined, boolean][] = [
    ['qwen2.5:7b', floor, true],
    ['QWEN2.5:7B-instruct', floor, true],
    ['amazon.nova-lite-v1:0', floor, true],
    ['qwen3:8b', floor, false],
    ['', floor, false],
    ['qwen2.5:7b', [], false],
    ['qwen2.5:7b', null, false],
    ['qwen2.5:7b', undefined, false],
  ]
  it.each(cases)('%s against %j', (model, f, want) => {
    expect(belowMissionFloor(model, f)).toBe(want)
  })
})
