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
    render(<div>{renderEvent(event({ unit: 0, passed: true }))}</div>)
    expect(screen.getByText('Unit 1 verification: passed')).toHaveClass('text-green-400')
    render(<div>{renderEvent(event({ unit: 2, passed: false }))}</div>)
    expect(screen.getByText('Unit 3 verification: failed')).toHaveClass('text-red-400')
  })

  it('omits the unit index when the payload has none, rather than showing "Unit ?"', () => {
    render(<div>{renderEvent(event({ passed: true }))}</div>)
    expect(screen.getByText('Unit verification: passed')).toBeInTheDocument()
  })
})

describe('mission.unit_regressed rendering', () => {
  it('names the unit 1-indexed with its title and failing check', () => {
    render(<div>{renderEvent(event({ unit: 0, title: 'write report', check: 'artifacts' }, 'mission.unit_regressed'))}</div>)
    expect(screen.getByText('Unit 1 (write report) regressed: artifacts check failed')).toHaveClass('text-red-400')
  })
})

describe('mission.finding_demoted rendering', () => {
  it('names the finding and the gate reason', () => {
    render(<div>{renderEvent(event({ title: 'wrong status', file: 'nope.md', reason: 'quotes no evidence' }, 'mission.finding_demoted'))}</div>)
    expect(screen.getByText('Finding demoted to minor: wrong status (quotes no evidence)')).toHaveClass('text-amber-400')
  })
})

describe('mission.generate_skipped rendering', () => {
  it('explains the skipped worker turn', () => {
    expect(renderEvent(event({}, 'mission.generate_skipped'))).toBe('Worker turn skipped: every unit is harness-verified')
  })
})

describe('mission.plan_created rendering', () => {
  it('shows unit count alone when the plan has no assumptions', () => {
    expect(renderEvent(event({ units: 2 }, 'mission.plan_created'))).toBe('Plan created with 2 unit(s)')
  })

  it('appends the assumption count when the plan declared any', () => {
    expect(
      renderEvent(
        event(
          { units: 2, assumptions: [{ assumption: 'no language version was specified', default: 'Python 3.12' }] },
          'mission.plan_created',
        ),
      ),
    ).toBe('Plan created with 2 unit(s), 1 assumption(s)')
  })
})

describe('mission.pushed rendering', () => {
  it('names the branch and remote host', () => {
    expect(renderEvent(event({ branch: 'mission/x', remote_host: 'github.com' }, 'mission.pushed'))).toBe(
      'Pushed mission/x to github.com',
    )
  })
})

describe('mission.push_failed rendering', () => {
  it('names the failure reason', () => {
    render(<div>{renderEvent(event({ reason: 'push rejected' }, 'mission.push_failed'))}</div>)
    expect(screen.getByText('Push failed: push rejected')).toHaveClass('text-red-400')
  })

  it('falls back to "unknown reason" when the payload has none', () => {
    render(<div>{renderEvent(event({}, 'mission.push_failed'))}</div>)
    expect(screen.getByText('Push failed: unknown reason')).toBeInTheDocument()
  })
})

describe('mission.pr_opened rendering', () => {
  it('renders a link to the PR number', () => {
    render(
      <div>
        {renderEvent(
          event({ url: 'https://github.com/octocat/hello-world/pull/9', number: 9 }, 'mission.pr_opened'),
        )}
      </div>,
    )
    const link = screen.getByRole('link', { name: '#9' })
    expect(link).toHaveAttribute('href', 'https://github.com/octocat/hello-world/pull/9')
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

describe('mission.steered rendering', () => {
  it('renders the operator note as an amber row', () => {
    render(<div>{renderEvent(event({ note: 'focus on staging next' }, 'mission.steered'))}</div>)
    const row = screen.getByText(/Operator note: focus on staging next/)
    expect(row).toBeInTheDocument()
    expect(row).toHaveClass('text-amber-400')
  })

  it('includes the phase when the payload carries one', () => {
    render(
      <div>{renderEvent(event({ note: 'hurry up', phase: 'discover' }, 'mission.steered'))}</div>,
    )
    expect(screen.getByText(/Operator note \(discover\): hurry up/)).toBeInTheDocument()
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

  it('renders executor.skipped with the resolve_failed error', () => {
    render(
      <div>
        {renderEvent(
          event({ harness: 'claude-cli', reason: 'resolve_failed', error: 'gateway unreachable' }, 'executor.skipped'),
        )}
      </div>,
    )
    expect(screen.getByText(/Harness skipped: resolve_failed/)).toBeInTheDocument()
    expect(screen.getByText(/gateway unreachable/)).toBeInTheDocument()
  })

  it('renders executor.skipped with the cooldown provider/model/until', () => {
    render(
      <div>
        {renderEvent(
          event(
            {
              harness: 'claude-cli',
              reason: 'cooldown',
              until: '2026-01-01T00:10:00Z',
              provider: 'anthropic',
              model: 'claude-haiku-4-5-20251001',
            },
            'executor.skipped',
          ),
        )}
      </div>,
    )
    expect(screen.getByText(/Harness skipped: cooldown/)).toBeInTheDocument()
    expect(screen.getByText(/anthropic\/claude-haiku-4-5-20251001 until 2026-01-01T00:10:00Z/)).toBeInTheDocument()
  })

  it('renders executor.skipped with the no_usable_entry skip reasons', () => {
    render(
      <div>
        {renderEvent(
          event(
            { harness: 'claude-cli', reason: 'no_usable_entry', skip_reasons: ['no credential configured'] },
            'executor.skipped',
          ),
        )}
      </div>,
    )
    expect(screen.getByText(/Harness skipped: no_usable_entry/)).toBeInTheDocument()
    expect(screen.getByText(/no credential configured/)).toBeInTheDocument()
  })
})

describe('mission.turn rendering', () => {
  it('renders phase, ok, and duration for a successful turn', () => {
    render(
      <div>{renderEvent(event({ phase: 'generate', duration_ms: 1500, ok: true, input: 'worker_retry' }, 'mission.turn'))}</div>,
    )
    expect(screen.getByText('Turn (generate): ok · 1.5s')).toBeInTheDocument()
  })

  it('renders a failed turn in red with the reason', () => {
    render(
      <div>
        {renderEvent(
          event({ phase: 'plan', duration_ms: 500, ok: false, input: 'worker_failed', reason: 'model returned empty' }, 'mission.turn'),
        )}
      </div>,
    )
    const row = screen.getByText(/Turn \(plan\): failed · 500ms/)
    expect(row).toHaveClass('text-red-400')
    expect(row).toHaveTextContent('model returned empty')
  })

  it('renders a legacy pre-rename phase name unchanged (historical events keep old names forever)', () => {
    render(
      <div>{renderEvent(event({ phase: 'execute', duration_ms: 800, ok: true, input: 'worker_retry' }, 'mission.turn'))}</div>,
    )
    expect(screen.getByText('Turn (execute): ok · 800ms')).toBeInTheDocument()
  })

  it('renders route and agent as muted context when the payload carries them', () => {
    render(
      <div>
        {renderEvent(
          event(
            { phase: 'generate', duration_ms: 1500, ok: true, input: 'worker_retry', route: 'coding', agent: 'Coder' },
            'mission.turn',
          ),
        )}
      </div>,
    )
    const row = screen.getByText(/Turn \(generate\): ok/)
    expect(row).toHaveTextContent('Coder · coding')
  })

  it('renders exactly as before when route/agent are absent (legacy event)', () => {
    render(
      <div>{renderEvent(event({ phase: 'generate', duration_ms: 1500, ok: true, input: 'worker_retry' }, 'mission.turn'))}</div>,
    )
    const row = screen.getByText('Turn (generate): ok · 1.5s')
    expect(row).toBeInTheDocument()
    expect(row).not.toHaveTextContent('undefined')
  })

  it('shows the served model in place of route when the payload carries one, with provider/route in the title', () => {
    render(
      <div>
        {renderEvent(
          event(
            {
              phase: 'prove', duration_ms: 42000, ok: true, input: 'review_approve',
              route: 'coding', agent: 'coder', provider: 'OpenAI Responses', model: 'gpt-5.3-codex',
            },
            'mission.turn',
          ),
        )}
      </div>,
    )
    const row = screen.getByText(/Turn \(prove\): ok · 42\.0s/)
    expect(row).toHaveTextContent('coder · gpt-5.3-codex')
    expect(row).not.toHaveTextContent('coding')
    const context = screen.getByText('coder · gpt-5.3-codex')
    expect(context).toHaveAttribute('title', 'OpenAI Responses / coding')
  })

  it('falls back to the agent · route form when model is absent (older events)', () => {
    render(
      <div>
        {renderEvent(
          event(
            { phase: 'generate', duration_ms: 1500, ok: true, input: 'worker_retry', route: 'coding', agent: 'Coder' },
            'mission.turn',
          ),
        )}
      </div>,
    )
    const context = screen.getByText('Coder · coding')
    expect(context).not.toHaveAttribute('title')
  })
})

describe('mission.discover_complete / mission.explore_complete rendering', () => {
  it('renders the new discover_complete kind', () => {
    expect(renderEvent(event({ chars: 120 }, 'mission.discover_complete'))).toBe('Discover complete (120 chars)')
  })

  it('renders the legacy explore_complete kind identically (historical tolerance)', () => {
    expect(renderEvent(event({ chars: 120 }, 'mission.explore_complete'))).toBe('Discover complete (120 chars)')
  })
})

describe('mission.result_complete rendering', () => {
  it('renders a successful result step summary', () => {
    render(
      <div>
        {renderEvent(
          event({ delivered: 2, artifacts_copied: 1, on_complete: 'push' }, 'mission.result_complete'),
        )}
      </div>,
    )
    const row = screen.getByText(/Result complete/)
    expect(row).toHaveClass('text-green-400')
    expect(row).toHaveTextContent('delivered to 2')
    expect(row).toHaveTextContent('1 artifact(s) copied')
    expect(row).toHaveTextContent('push')
  })

  it('renders a failed result step in red', () => {
    render(<div>{renderEvent(event({ delivery_error: 'destination unreachable' }, 'mission.result_complete'))}</div>)
    expect(screen.getByText(/Result step failed/)).toHaveClass('text-red-400')
  })
})

describe('mission.retry rendering', () => {
  it('shows the cause and reason', () => {
    render(<div>{renderEvent(event({ cause: 'worker_failed', reason: 'transport_death' }, 'mission.retry'))}</div>)
    const row = screen.getByText(/Retrying \(worker_failed\)/)
    expect(row).toHaveClass('text-amber-400')
    expect(row).toHaveTextContent('transport_death')
  })

  it('falls back to a bare "Retrying" when the payload has no cause', () => {
    render(<div>{renderEvent(event({}, 'mission.retry'))}</div>)
    expect(screen.getByText('Retrying')).toHaveClass('text-amber-400')
  })
})

describe('mission.permission_denied rendering', () => {
  it('shows the tool and a trimmed detail', () => {
    render(<div>{renderEvent(event({ tool: 'shell', detail: 'rm -rf /workspace' }, 'mission.permission_denied'))}</div>)
    const row = screen.getByText(/Permission denied: shell/)
    expect(row).toHaveClass('text-red-400')
    expect(row).toHaveTextContent('rm -rf /workspace')
  })
})

describe('unknown event kind fallback', () => {
  it('falls back to the raw kind string', () => {
    expect(renderEvent(event({}, 'some.future.kind'))).toBe('some.future.kind')
  })
})
