import { describe, expect, it } from 'vitest'
import { prettyArgs, summarizeTools, totalDuration } from './activity'
import type { ToolRun } from './chat'

describe('prettyArgs', () => {
  it('pretty-prints valid JSON', () => {
    expect(prettyArgs('{"a":1}')).toBe('{\n  "a": 1\n}')
  })

  it('returns the raw string when it is not valid JSON', () => {
    expect(prettyArgs('not json')).toBe('not json')
  })
})

describe('totalDuration', () => {
  it('sums durations, treating missing duration as zero', () => {
    const tools: ToolRun[] = [
      { id: '1', name: 'a', status: 'ok', durationMs: 100 },
      { id: '2', name: 'b', status: 'running' },
      { id: '3', name: 'c', status: 'ok', durationMs: 50 },
    ]
    expect(totalDuration(tools)).toBe(150)
  })

  it('returns 0 for an empty list', () => {
    expect(totalDuration([])).toBe(0)
  })
})

describe('summarizeTools', () => {
  it('joins up to three distinct tool names in first-appearance order', () => {
    const tools: ToolRun[] = [
      { id: '1', name: 'a', status: 'ok' },
      { id: '2', name: 'b', status: 'ok' },
      { id: '3', name: 'c', status: 'ok' },
    ]
    expect(summarizeTools(tools)).toBe('a, b, c')
  })

  it('dedupes and counts repeated calls', () => {
    const tools: ToolRun[] = [
      { id: '1', name: 'a', status: 'ok' },
      { id: '2', name: 'a', status: 'ok' },
    ]
    expect(summarizeTools(tools)).toBe('2× a')
  })

  it('caps the list and shows a +N more suffix beyond three', () => {
    const tools: ToolRun[] = [
      { id: '1', name: 'a', status: 'ok' },
      { id: '2', name: 'b', status: 'ok' },
      { id: '3', name: 'c', status: 'ok' },
      { id: '4', name: 'd', status: 'ok' },
      { id: '5', name: 'e', status: 'ok' },
    ]
    expect(summarizeTools(tools)).toBe('a, b, c, +2 more')
  })

  it('returns an empty string for no tools', () => {
    expect(summarizeTools([])).toBe('')
  })
})
