import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { PageShell } from './page-shell'

describe('PageShell', () => {
  it('exposes the width variant as data-width', () => {
    render(
      <PageShell width="reading">
        <p>content</p>
      </PageShell>,
    )
    expect(screen.getByText('content').parentElement).toHaveAttribute('data-width', 'reading')
  })

  it('defaults to full width with no max-width class', () => {
    render(
      <PageShell>
        <p>content</p>
      </PageShell>,
    )
    const shell = screen.getByText('content').parentElement
    expect(shell).toHaveAttribute('data-width', 'full')
    expect(shell?.className).not.toMatch(/max-w-/)
  })
})
