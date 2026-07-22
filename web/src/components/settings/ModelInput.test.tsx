import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ModelInput } from './ModelInput'

afterEach(cleanup)

describe('ModelInput', () => {
  it('shows the friendly name primary and the raw id secondary', () => {
    render(
      <ModelInput
        value=""
        onChange={vi.fn()}
        suggestions={[{ id: 'amazon.nova-lite-v1:0', name: 'Nova Lite' }]}
      />,
    )
    fireEvent.focus(screen.getByRole('textbox'))
    expect(screen.getByText('Nova Lite')).toBeInTheDocument()
    expect(screen.getByText('amazon.nova-lite-v1:0')).toBeInTheDocument()
  })

  it('shows only the id when a suggestion has no name', () => {
    render(<ModelInput value="" onChange={vi.fn()} suggestions={[{ id: 'gpt-5.6-sol' }]} />)
    fireEvent.focus(screen.getByRole('textbox'))
    expect(screen.getByText('gpt-5.6-sol')).toBeInTheDocument()
  })

  it('filters suggestions by name as well as id', () => {
    render(
      <ModelInput
        value="nova lite"
        onChange={vi.fn()}
        suggestions={[
          { id: 'amazon.nova-lite-v1:0', name: 'Nova Lite' },
          { id: 'amazon.nova-pro-v1:0', name: 'Nova Pro' },
        ]}
      />,
    )
    fireEvent.focus(screen.getByRole('textbox'))
    expect(screen.getByText('Nova Lite')).toBeInTheDocument()
    expect(screen.queryByText('Nova Pro')).not.toBeInTheDocument()
  })

  it('picking a suggestion submits the id, not the name', () => {
    const onChange = vi.fn()
    render(
      <ModelInput
        value=""
        onChange={onChange}
        suggestions={[{ id: 'amazon.nova-lite-v1:0', name: 'Nova Lite' }]}
      />,
    )
    fireEvent.focus(screen.getByRole('textbox'))
    fireEvent.click(screen.getByText('Nova Lite'))
    expect(onChange).toHaveBeenCalledWith('amazon.nova-lite-v1:0')
  })
})
