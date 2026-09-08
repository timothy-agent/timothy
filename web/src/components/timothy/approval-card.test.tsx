import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { ApprovalCardShell, useGateShortcuts } from './approval-card'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'

function Harness({ onA, onD }: { onA: () => void; onD: () => void }) {
  const onKeyDown = useGateShortcuts({ a: onA, d: onD })
  return (
    <ApprovalCardShell
      title="Timothy wants to run a command"
      badges={<Badge variant="destructive">destructive</Badge>}
      meta="08:04"
      rationale="Confirms the change did not regress tests."
      consequence="Runs in the sandbox."
      onKeyDown={onKeyDown}
      actions={
        <>
          <Button variant="outline" onClick={onD}>
            Deny
          </Button>
          <Button onClick={onA}>Allow once</Button>
        </>
      }
    >
      <div>body content</div>
    </ApprovalCardShell>
  )
}

describe('ApprovalCardShell', () => {
  it('renders title, badges, children, rationale, and consequence', () => {
    render(<Harness onA={() => {}} onD={() => {}} />)
    expect(screen.getByText('Timothy wants to run a command')).toBeInTheDocument()
    expect(screen.getByText('destructive')).toBeInTheDocument()
    expect(screen.getByText('body content')).toBeInTheDocument()
    expect(screen.getByText('Confirms the change did not regress tests.')).toBeInTheDocument()
    expect(screen.getByText('Runs in the sandbox.')).toBeInTheDocument()
    expect(screen.getByText('08:04')).toBeInTheDocument()
    expect(screen.getByText('Waiting')).toBeInTheDocument()
  })

  it('renders actions when no answer is set', () => {
    render(<Harness onA={() => {}} onD={() => {}} />)
    expect(screen.getByRole('button', { name: 'Allow once' })).toBeInTheDocument()
  })

  it('replaces actions with answered content when set', () => {
    render(
      <ApprovalCardShell
        title="Timothy wants to run a command"
        actions={<Button>Allow once</Button>}
        answered={<p>Allowed once</p>}
      />,
    )
    expect(screen.queryByRole('button', { name: 'Allow once' })).not.toBeInTheDocument()
    expect(screen.getByText('Allowed once')).toBeInTheDocument()
  })

  it('renders nothing for the decision row when actions and answered are both absent', () => {
    const { container } = render(<ApprovalCardShell title="Timothy wants to use a tool" />)
    expect(container.querySelectorAll('button')).toHaveLength(0)
  })

  it('is a labelled region', () => {
    render(<Harness onA={() => {}} onD={() => {}} />)
    expect(screen.getByRole('region', { name: 'Timothy wants to run a command' })).toBeInTheDocument()
  })
})

describe('useGateShortcuts', () => {
  it('fires the mapped handler on keydown', () => {
    const onA = vi.fn()
    const onD = vi.fn()
    render(<Harness onA={onA} onD={onD} />)
    fireEvent.keyDown(screen.getByRole('region'), { key: 'a' })
    expect(onA).toHaveBeenCalledTimes(1)
    expect(onD).not.toHaveBeenCalled()
  })

  it('ignores keydown when a textarea is focused', () => {
    const onA = vi.fn()
    function TextareaHarness() {
      const onKeyDown = useGateShortcuts({ a: onA })
      return (
        <div onKeyDown={onKeyDown}>
          <textarea data-testid="note" />
        </div>
      )
    }
    render(<TextareaHarness />)
    fireEvent.keyDown(screen.getByTestId('note'), { key: 'a' })
    expect(onA).not.toHaveBeenCalled()
  })

  it('ignores keydown when an input is focused', () => {
    const onA = vi.fn()
    function InputHarness() {
      const onKeyDown = useGateShortcuts({ a: onA })
      return (
        <div onKeyDown={onKeyDown}>
          <input data-testid="note" />
        </div>
      )
    }
    render(<InputHarness />)
    fireEvent.keyDown(screen.getByTestId('note'), { key: 'a' })
    expect(onA).not.toHaveBeenCalled()
  })

  it('ignores keydown with a modifier held', () => {
    const onA = vi.fn()
    render(<Harness onA={onA} onD={() => {}} />)
    fireEvent.keyDown(screen.getByRole('region'), { key: 'a', metaKey: true })
    expect(onA).not.toHaveBeenCalled()
  })
})
