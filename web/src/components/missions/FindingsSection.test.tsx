import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { FindingsSection } from './FindingsSection'

afterEach(cleanup)

describe('FindingsSection', () => {
  it('renders nothing for an empty ledger', () => {
    const { container } = render(<FindingsSection findings={[]} />)
    expect(container).toBeEmptyDOMElement()
  })

  it('lists id, severity, file, title and the evidence line', () => {
    render(
      <FindingsSection
        findings={[
          {
            id: 'F1',
            unit: 0,
            title: 'missing validation',
            file: 'x.go',
            detail: 'no input check',
            severity: 'blocking',
            evidence: '+func handle(r *http.Request) {',
            status: 'open',
          },
          { id: 'F2', unit: 0, title: 'style nit', file: '', detail: '', severity: 'minor', status: 'resolved' },
        ]}
      />,
    )
    expect(screen.getByText('F1')).toBeInTheDocument()
    expect(screen.getByText('blocking')).toBeInTheDocument()
    expect(screen.getByText('x.go')).toBeInTheDocument()
    expect(screen.getByText('missing validation')).toBeInTheDocument()
    expect(screen.getByText('+func handle(r *http.Request) {')).toBeInTheDocument()
    expect(screen.getByText('style nit').closest('li')).toHaveClass('line-through')
  })
})
