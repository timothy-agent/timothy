import { describe, expect, it } from 'vitest'
import type { MissionEvent } from '../../api/types'
import { renderEvent } from './eventRenderers'

function event(payload: unknown): MissionEvent {
  return {
    mission_id: 'm1',
    seq: 1,
    kind: 'mission.unit_verified',
    payload,
    provenance: 'harness',
    created_at: '2026-01-01T00:00:00Z',
  }
}

describe('mission.unit_verified rendering', () => {
  it('names the unit 1-indexed when the payload carries a 0-indexed index', () => {
    expect(renderEvent(event({ unit: 0, passed: true }))).toBe('Unit 1 verification: passed')
    expect(renderEvent(event({ unit: 2, passed: false }))).toBe('Unit 3 verification: failed')
  })

  it('omits the unit index when the payload has none, rather than showing "Unit ?"', () => {
    expect(renderEvent(event({ passed: true }))).toBe('Unit verification: passed')
  })
})
