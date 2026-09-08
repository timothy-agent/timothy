import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { HarnessIcon, harnessLabel } from './HarnessIcon'

describe('harnessLabel', () => {
  it('falls back to Native when no harness is given', () => {
    expect(harnessLabel(undefined)).toBe('Native')
  })

  it('maps known harness ids to display names', () => {
    expect(harnessLabel('claude-cli')).toBe('Claude Code')
    expect(harnessLabel('codex-cli')).toBe('Codex CLI')
  })

  it('falls back to the raw id for an unknown harness', () => {
    expect(harnessLabel('some-new-harness')).toBe('some-new-harness')
  })
})

describe('HarnessIcon', () => {
  it('renders the brand mark for a native mission', () => {
    const { container } = render(<HarnessIcon />)
    expect(container.querySelector('svg[aria-hidden="true"]')).toBeInTheDocument()
  })

  it('renders without throwing for every known harness', () => {
    for (const harness of ['pi', 'codex-cli', 'opencode', 'claude-cli', 'cursor-cli']) {
      const { container, unmount } = render(<HarnessIcon harness={harness} />)
      expect(container.firstChild).not.toBeNull()
      unmount()
    }
  })
})
