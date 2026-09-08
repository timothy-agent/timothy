import { describe, expect, it } from 'vitest'
import { missionStatus, toolCallStatus } from './status'

describe('missionStatus', () => {
  it('maps idle to neutral', () => {
    expect(missionStatus({ status: 'idle' })).toBe('neutral')
  })

  it('maps running phases to working', () => {
    for (const phase of ['discover', 'plan', 'generate', 'prove']) {
      expect(missionStatus({ phase, status: 'working' })).toBe('working')
    }
  })

  it('maps waiting_for_input to waiting', () => {
    expect(missionStatus({ status: 'waiting_for_input' })).toBe('waiting')
  })

  it('maps paused to warning', () => {
    expect(missionStatus({ status: 'paused' })).toBe('warning')
  })

  it('maps phase done to success', () => {
    expect(missionStatus({ phase: 'done', status: 'working' })).toBe('success')
  })

  it('maps phase failed to error', () => {
    expect(missionStatus({ phase: 'failed', status: 'error' })).toBe('error')
  })

  it('maps a cancelled failure to neutral', () => {
    expect(missionStatus({ phase: 'failed', status: 'cancelled' })).toBe('neutral')
  })

  it('maps a non-terminal error status to error', () => {
    expect(missionStatus({ phase: 'generate', status: 'error' })).toBe('error')
  })

  it('maps a non-terminal done status to success', () => {
    expect(missionStatus({ phase: 'generate', status: 'done' })).toBe('success')
  })
})

describe('toolCallStatus', () => {
  it('maps running to working', () => {
    expect(toolCallStatus('running')).toBe('working')
  })

  it('maps ok to success', () => {
    expect(toolCallStatus('ok')).toBe('success')
  })

  it('maps denied to neutral', () => {
    expect(toolCallStatus('denied')).toBe('neutral')
  })

  it('maps error to error', () => {
    expect(toolCallStatus('error')).toBe('error')
  })

  it('maps blocked to error', () => {
    expect(toolCallStatus('blocked')).toBe('error')
  })
})
