import { describe, expect, it } from 'vitest'
import { sampleMissionInput } from './sampleMission'

describe('sampleMissionInput', () => {
  it('is a light general mission', () => {
    const input = sampleMissionInput()
    expect(input.kind).toBe('general')
    expect(input.light).toBe(true)
  })

  it('tags the goal with the local time', () => {
    const input = sampleMissionInput(new Date(2026, 9, 8, 7, 5, 9))
    expect(input.goal).toContain('Run tag: onboarding-20261008-070509.')
    expect(input.goal).toMatch(/^Write a short guide, under 200 words/)
  })

  it('gives different goals at different times', () => {
    const a = sampleMissionInput(new Date(2026, 9, 8, 7, 5, 9))
    const b = sampleMissionInput(new Date(2026, 9, 8, 7, 5, 10))
    expect(a.goal).not.toBe(b.goal)
  })
})
