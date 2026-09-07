import { fireEvent, render, screen } from '@testing-library/react'
import { useState } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { ConfirmDialog, useConfirm } from './confirm-dialog'

function Harness({ onConfirm, loading = false, destructive = false }: { onConfirm: () => void; loading?: boolean; destructive?: boolean }) {
  const [open, setOpen] = useState(true)
  return (
    <ConfirmDialog
      open={open}
      onOpenChange={setOpen}
      title="Delete mission"
      description="This cannot be undone."
      destructive={destructive}
      loading={loading}
      onConfirm={onConfirm}
    />
  )
}

describe('ConfirmDialog', () => {
  it('opens with title and description visible', () => {
    render(<Harness onConfirm={() => {}} />)
    expect(screen.getByText('Delete mission')).toBeInTheDocument()
    expect(screen.getByText('This cannot be undone.')).toBeInTheDocument()
  })

  it('closes on cancel', () => {
    render(<Harness onConfirm={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByText('Delete mission')).not.toBeInTheDocument()
  })

  it('calls onConfirm when the confirm button is clicked', () => {
    const onConfirm = vi.fn()
    render(<Harness onConfirm={onConfirm} />)
    fireEvent.click(screen.getByRole('button', { name: 'Confirm' }))
    expect(onConfirm).toHaveBeenCalledOnce()
  })

  it('disables both buttons and sets aria-busy while loading', () => {
    render(<Harness onConfirm={() => {}} loading />)
    const confirm = screen.getByRole('button', { name: 'Confirm' })
    const cancel = screen.getByRole('button', { name: 'Cancel' })
    expect(confirm).toBeDisabled()
    expect(confirm).toHaveAttribute('aria-busy', 'true')
    expect(cancel).toBeDisabled()
  })

  it('sets data-variant=destructive on the confirm button in destructive mode', () => {
    render(<Harness onConfirm={() => {}} destructive />)
    expect(screen.getByRole('button', { name: 'Confirm' })).toHaveAttribute('data-variant', 'destructive')
  })
})

describe('useConfirm', () => {
  function ConfirmHarness() {
    const [confirm, dialog] = useConfirm()
    const [result, setResult] = useState<string>('')
    return (
      <div>
        <button
          onClick={async () => {
            const ok = await confirm({ title: 'Cancel mission?', description: 'This stops the run.' })
            setResult(ok ? 'confirmed' : 'cancelled')
          }}
        >
          Trigger
        </button>
        <p>{result}</p>
        {dialog}
      </div>
    )
  }

  it('resolves true when the user confirms', async () => {
    render(<ConfirmHarness />)
    fireEvent.click(screen.getByRole('button', { name: 'Trigger' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Confirm' }))
    expect(await screen.findByText('confirmed')).toBeInTheDocument()
  })
})
