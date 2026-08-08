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
  const resultPayload = (cost_usd: number | null, cost_usd_billed = false) => ({
    status: 'ok',
    is_error: false,
    duration_ms: 1500,
    exit_code: 0,
    parse: 'json',
    denials: [],
    usage: { input_tokens: 100, output_tokens: 50, cost_usd, cost_usd_billed },
  })

  it('shows the billed cost plainly when cost_usd_billed is true (anthropic api_key)', () => {
    render(<div>{renderEvent(event(resultPayload(0.1234, true), 'executor.result'))}</div>)
    expect(screen.getByText(/Harness finished:/)).toBeInTheDocument()
    expect(screen.getByText(/\$0\.1234/)).toBeInTheDocument()
    expect(screen.queryByText(/not billed/)).not.toBeInTheDocument()
    expect(screen.queryByText(/harness-reported/)).not.toBeInTheDocument()
  })

  it('flags a subscription-auth run as unbilled when cost_usd is a number', () => {
    const spawn = event(
      { harness: 'claude-cli', provider: 'anthropic', model: 'sonnet', auth_mode: 'subscription', run_id: 'r1' },
      'executor.spawned',
      1,
    )
    const result = event(resultPayload(0.2534), 'executor.result', 2)
    render(<div>{renderEvent(result, [spawn, result])}</div>)
    expect(screen.getByText(/\$0\.2534 · subscription \(not billed\)/)).toBeInTheDocument()
  })

  it('flags an oauth_token-auth run as unbilled when cost_usd is a number', () => {
    const spawn = event(
      { harness: 'claude-cli', provider: 'anthropic', model: 'sonnet', auth_mode: 'oauth_token', run_id: 'r1' },
      'executor.spawned',
      1,
    )
    const result = event(resultPayload(0.2534), 'executor.result', 2)
    render(<div>{renderEvent(result, [spawn, result])}</div>)
    expect(screen.getByText(/\$0\.2534 · subscription \(not billed\)/)).toBeInTheDocument()
  })

  it('labels a non-anthropic api_key run\'s cost_usd as harness-reported, never presented as billed', () => {
    const spawn = event(
      { harness: 'claude-cli', provider: 'GLM (Z.ai)', model: 'glm-4.7', auth_mode: 'api_key', run_id: 'r1' },
      'executor.spawned',
      1,
    )
    // cost_usd_billed omitted (false): the ledger priced this run from
    // GLM's own rows, not the CLI's Anthropic-priced total_cost_usd.
    const result = event(resultPayload(0.0727), 'executor.result', 2)
    render(<div>{renderEvent(result, [spawn, result])}</div>)
    expect(screen.getByText(/harness-reported \$0\.0727/)).toBeInTheDocument()
    expect(screen.queryByText(/not billed/)).not.toBeInTheDocument()
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
    expect(screen.getByText('harness')).toBeInTheDocument()
  })

  it('renders executor.died as an error row', () => {
    render(<div>{renderEvent(event({ reason: 'oom' }, 'executor.died'))}</div>)
    expect(screen.getByText(/Harness died: oom/)).toBeInTheDocument()
  })

  it('renders executor.idle_killed as an error row', () => {
    render(<div>{renderEvent(event({ idle_s: 120 }, 'executor.idle_killed'))}</div>)
    expect(screen.getByText(/Harness killed: idle for 120s/)).toBeInTheDocument()
  })

  it('renders executor.auth_failed with a re-login hint', () => {
    render(<div>{renderEvent(event({ harness: 'claude-cli' }, 'executor.auth_failed'))}</div>)
    expect(screen.getByText(/re-run the harness login/)).toBeInTheDocument()
  })
})

describe('unknown event kind fallback', () => {
  it('falls back to the raw kind string', () => {
    expect(renderEvent(event({}, 'some.future.kind'))).toBe('some.future.kind')
  })
})
