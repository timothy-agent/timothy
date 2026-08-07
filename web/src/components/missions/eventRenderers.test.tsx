import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import type { MissionEvent } from '../../api/types'
import { renderEvent } from './eventRenderers'

function event(payload: unknown, kind = 'mission.unit_verified', seq = 1): MissionEvent {
  return {
    mission_id: 'm1',
    seq,
    kind,
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

describe('executor.result rendering', () => {
  const resultPayload = (cost_usd: number | null) => ({
    status: 'ok',
    is_error: false,
    duration_ms: 1500,
    exit_code: 0,
    parse: 'json',
    denials: [],
    usage: { input_tokens: 100, output_tokens: 50, cost_usd },
  })

  it('shows the billed cost when cost_usd is a number', () => {
    render(<div>{renderEvent(event(resultPayload(0.1234), 'executor.result'))}</div>)
    expect(screen.getByText(/\$0\.1234/)).toBeInTheDocument()
  })

  it('shows subscription untracked cost when cost_usd is null and the spawn was subscription auth', () => {
    const spawn = event(
      { harness: 'claude-cli', provider: 'anthropic', model: 'sonnet', auth_mode: 'subscription', run_id: 'r1' },
      'executor.spawned',
      1,
    )
    const result = event(resultPayload(null), 'executor.result', 2)
    render(<div>{renderEvent(result, [spawn, result])}</div>)
    expect(screen.getByText(/subscription — cost untracked/)).toBeInTheDocument()
  })

  it('shows subscription untracked cost when cost_usd is null and the spawn was oauth_token auth', () => {
    const spawn = event(
      { harness: 'claude-cli', provider: 'anthropic', model: 'sonnet', auth_mode: 'oauth_token', run_id: 'r1' },
      'executor.spawned',
      1,
    )
    const result = event(resultPayload(null), 'executor.result', 2)
    render(<div>{renderEvent(result, [spawn, result])}</div>)
    expect(screen.getByText(/subscription — cost untracked/)).toBeInTheDocument()
  })

  it('shows cost unreported when cost_usd is null and auth was not subscription', () => {
    const spawn = event(
      { harness: 'claude-cli', provider: 'anthropic', model: 'sonnet', auth_mode: 'api_key', run_id: 'r1' },
      'executor.spawned',
      1,
    )
    const result = event(resultPayload(null), 'executor.result', 2)
    render(<div>{renderEvent(result, [spawn, result])}</div>)
    expect(screen.getByText(/cost unreported/)).toBeInTheDocument()
  })

  it('lists denials when present', () => {
    const result = event({ ...resultPayload(0), denials: ['rm -rf /'] }, 'executor.result')
    render(<div>{renderEvent(result)}</div>)
    expect(screen.getByText(/denials: rm -rf \//)).toBeInTheDocument()
  })
})

describe('executor lifecycle event rendering', () => {
  it('renders executor.spawned with harness, provider/model, and auth mode', () => {
    render(
      <div>
        {renderEvent(
          event(
            { harness: 'claude-cli', provider: 'anthropic', model: 'sonnet', auth_mode: 'api_key', run_id: 'r1' },
            'executor.spawned',
          ),
        )}
      </div>,
    )
    expect(screen.getByText(/Delegated to claude-cli/)).toBeInTheDocument()
    expect(screen.getByText('anthropic/sonnet')).toBeInTheDocument()
  })

  it('renders executor.died as an error row', () => {
    render(<div>{renderEvent(event({ reason: 'oom' }, 'executor.died'))}</div>)
    expect(screen.getByText(/Executor died: oom/)).toBeInTheDocument()
  })

  it('renders executor.idle_killed as an error row', () => {
    render(<div>{renderEvent(event({ idle_s: 120 }, 'executor.idle_killed'))}</div>)
    expect(screen.getByText(/idle for 120s/)).toBeInTheDocument()
  })

  it('renders executor.auth_failed with a re-login hint', () => {
    render(<div>{renderEvent(event({ harness: 'claude-cli' }, 'executor.auth_failed'))}</div>)
    expect(screen.getByText(/re-run the executor login/)).toBeInTheDocument()
  })
})

describe('unknown event kind fallback', () => {
  it('falls back to the raw kind string', () => {
    expect(renderEvent(event({}, 'some.future.kind'))).toBe('some.future.kind')
  })
})
