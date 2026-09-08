import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { failedPhaseFromEvents, PhaseStepper, normalizePhase, phaseIndex, phaseStepText } from './PhaseStepper'

describe('normalizePhase', () => {
  it('passes current phase names through', () => {
    expect(normalizePhase('build')).toBe('build')
  })

  it('maps legacy phase names', () => {
    expect(normalizePhase('execute')).toBe('build')
    expect(normalizePhase('explore')).toBe('discover')
    expect(normalizePhase('review')).toBe('prove')
  })

  // Missions created before #611 stored 'generate'; those rows are
  // append-only, so the mapping has to survive.
  it('maps the pre-#611 generate name onto build', () => {
    expect(normalizePhase('generate')).toBe('build')
  })

  it('passes terminal phases through', () => {
    expect(normalizePhase('done')).toBe('done')
    expect(normalizePhase('failed')).toBe('failed')
  })

  it('falls back to discover for an unrecognized phase', () => {
    expect(normalizePhase('bogus')).toBe('discover')
  })
})

describe('phaseIndex', () => {
  it('indexes the five pipeline phases', () => {
    expect(phaseIndex('discover')).toBe(0)
    expect(phaseIndex('plan')).toBe(1)
    expect(phaseIndex('build')).toBe(2)
    expect(phaseIndex('prove')).toBe(3)
    expect(phaseIndex('result')).toBe(4)
  })

  it('indexes legacy names via their current mapping', () => {
    expect(phaseIndex('execute')).toBe(2)
  })

  it('returns 5 for terminal phases', () => {
    expect(phaseIndex('done')).toBe(5)
    expect(phaseIndex('failed')).toBe(5)
  })
})

describe('phaseStepText', () => {
  it('renders "Phase · n of 5"', () => {
    expect(phaseStepText({ phase: 'discover' })).toBe('Discover · 1 of 5')
    expect(phaseStepText({ phase: 'prove' })).toBe('Prove · 4 of 5')
  })

  it('renders light missions as "Build · light"', () => {
    expect(phaseStepText({ phase: 'build', light: true })).toBe('Build · light')
  })

  it('renders terminal phases as Done/Failed', () => {
    expect(phaseStepText({ phase: 'done' })).toBe('Done')
    expect(phaseStepText({ phase: 'failed' })).toBe('Failed')
  })

  it('renders just the label for an unrecognized phase', () => {
    expect(phaseStepText({ phase: 'bogus' })).toBe('bogus')
  })
})

describe('PhaseStepper', () => {
  it('renders all five steps with the current one marked', () => {
    render(<PhaseStepper phase="build" />)
    const current = screen.getByText('Build').closest('span[aria-current="step"]')
    expect(current).not.toBeNull()
    expect(screen.getByText('Discover')).toBeInTheDocument()
    expect(screen.getByText('Result')).toBeInTheDocument()
  })

  it('marks completed steps with a check icon', () => {
    const { container } = render(<PhaseStepper phase="prove" />)
    // discover, plan, build are completed (3 checks); prove is current (no check)
    const checks = container.querySelectorAll('svg.lucide-check')
    expect(checks).toHaveLength(3)
    checks.forEach((c) => expect(c).toHaveClass('text-good'))
  })

  it('renders terminal phases with all steps completed and no status badge of its own', () => {
    const { container } = render(<PhaseStepper phase="done" />)
    expect(screen.queryByText('Done')).not.toBeInTheDocument()
    expect(container.querySelectorAll('svg.lucide-check')).toHaveLength(5)
    expect(screen.getAllByText(/Discover|Plan|Build|Prove|Result/)).toHaveLength(5)
  })

  it('marks the failed step with an X and checks the ones before it', () => {
    const { container } = render(<PhaseStepper phase="failed" failedAt="build" />)
    expect(container.querySelectorAll('svg.lucide-check')).toHaveLength(2)
    const failedStep = screen.getByText('Build').closest('span')
    expect(failedStep).toHaveClass('text-destructive')
    expect(failedStep?.querySelector('svg.lucide-circle-x')).not.toBeNull()
    expect(screen.getByText('Prove').closest('span')).toHaveClass('text-muted-foreground')
  })

  it('derives the failed phase from the latest phase_started event', () => {
    const ev = (seq: number, kind: string, payload: unknown) => ({
      mission_id: 'm',
      seq,
      kind,
      payload,
      provenance: 'harness',
      created_at: '2026-01-01T00:00:00Z',
    })
    expect(failedPhaseFromEvents([])).toBe('discover')
    expect(failedPhaseFromEvents([], true)).toBe('build')
    expect(
      failedPhaseFromEvents([
        ev(1, 'mission.phase_started', { phase: 'plan' }),
        ev(2, 'mission.phase_started', { phase: 'execute' }),
        ev(3, 'mission.failed', { reason: 'cancelled' }),
      ]),
    ).toBe('build')
  })

  it('renders failed phase with no completed steps', () => {
    const { container } = render(<PhaseStepper phase="failed" />)
    expect(screen.queryByText('Failed')).not.toBeInTheDocument()
    expect(container.querySelectorAll('svg.lucide-check')).toHaveLength(0)
    expect(screen.getAllByText(/Discover|Plan|Build|Prove|Result/)).toHaveLength(5)
  })

  it('renders a single Build step with a light badge for light missions', () => {
    render(<PhaseStepper phase="build" light />)
    expect(screen.getByText('Build')).toBeInTheDocument()
    expect(screen.getByText('light')).toBeInTheDocument()
    expect(screen.queryByText('Discover')).not.toBeInTheDocument()
  })

  it('is an ordered list labelled Mission phases', () => {
    render(<PhaseStepper phase="plan" />)
    expect(screen.getByRole('list', { name: 'Mission phases' })).toBeInTheDocument()
  })

  it('marks a light mission done with a green check and failed with an X', () => {
    const done = render(<PhaseStepper phase="done" light />)
    expect(done.container.querySelector('svg.lucide-check')).toHaveClass('text-good')
    done.unmount()

    const failed = render(<PhaseStepper phase="failed" light />)
    expect(failed.container.querySelector('svg.lucide-circle-x')).not.toBeNull()
    expect(screen.getByText('Build').closest('span')).toHaveClass('text-destructive')
    failed.unmount()

    const running = render(<PhaseStepper phase="build" light />)
    expect(running.container.querySelector('svg.lucide-check')).toBeNull()
    expect(running.container.querySelector('svg.lucide-circle-x')).toBeNull()
  })
})
