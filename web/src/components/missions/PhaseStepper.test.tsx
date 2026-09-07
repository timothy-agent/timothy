import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { PhaseStepper, normalizePhase, phaseIndex, phaseStepText } from './PhaseStepper'

describe('normalizePhase', () => {
  it('passes current phase names through', () => {
    expect(normalizePhase('generate')).toBe('generate')
  })

  it('maps legacy phase names', () => {
    expect(normalizePhase('execute')).toBe('generate')
    expect(normalizePhase('explore')).toBe('discover')
    expect(normalizePhase('review')).toBe('prove')
  })

  it('passes terminal phases through', () => {
    expect(normalizePhase('done')).toBe('done')
    expect(normalizePhase('failed')).toBe('failed')
  })
})

describe('phaseIndex', () => {
  it('indexes the five pipeline phases', () => {
    expect(phaseIndex('discover')).toBe(0)
    expect(phaseIndex('plan')).toBe(1)
    expect(phaseIndex('generate')).toBe(2)
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

  it('renders light missions as "Generate · light"', () => {
    expect(phaseStepText({ phase: 'generate', light: true })).toBe('Generate · light')
  })

  it('renders terminal phases as Done/Failed', () => {
    expect(phaseStepText({ phase: 'done' })).toBe('Done')
    expect(phaseStepText({ phase: 'failed' })).toBe('Failed')
  })
})

describe('PhaseStepper', () => {
  it('renders all five steps with the current one marked', () => {
    render(<PhaseStepper phase="generate" status="working" />)
    const current = screen.getByText('Generate').closest('span[aria-current="step"]')
    expect(current).not.toBeNull()
    expect(screen.getByText('Discover')).toBeInTheDocument()
    expect(screen.getByText('Result')).toBeInTheDocument()
  })

  it('marks completed steps with a check icon', () => {
    const { container } = render(<PhaseStepper phase="prove" status="working" />)
    // discover, plan, generate are completed (3 checks); prove is current (no check)
    expect(container.querySelectorAll('svg.lucide-check')).toHaveLength(3)
  })

  it('renders terminal phases with all steps completed and a status badge', () => {
    render(<PhaseStepper phase="done" status="success" />)
    expect(screen.getByText('Done')).toBeInTheDocument()
    expect(screen.getAllByText(/Discover|Plan|Generate|Prove|Result/)).toHaveLength(5)
  })

  it('renders failed phase with no completed steps, only the status badge', () => {
    const { container } = render(<PhaseStepper phase="failed" status="error" />)
    expect(screen.getByText('Failed')).toBeInTheDocument()
    expect(container.querySelectorAll('svg.lucide-check')).toHaveLength(0)
    expect(screen.getAllByText(/Discover|Plan|Generate|Prove|Result/)).toHaveLength(5)
  })

  it('renders a single Generate step with a light badge for light missions', () => {
    render(<PhaseStepper phase="generate" status="working" light />)
    expect(screen.getByText('Generate')).toBeInTheDocument()
    expect(screen.getByText('light')).toBeInTheDocument()
    expect(screen.queryByText('Discover')).not.toBeInTheDocument()
  })

  it('is an ordered list labelled Mission phases', () => {
    render(<PhaseStepper phase="plan" status="waiting" />)
    expect(screen.getByRole('list', { name: 'Mission phases' })).toBeInTheDocument()
  })
})
