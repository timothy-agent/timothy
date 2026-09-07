import { fireEvent, render, screen } from '@testing-library/react'
import { useState } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { Combobox, type ComboboxOption } from './combobox'

// jsdom lacks scrollIntoView; cmdk calls it when the selected item changes.
beforeEach(() => {
  Element.prototype.scrollIntoView = vi.fn()
})

const options: ComboboxOption[] = [
  { value: 'gpt-5', label: 'gpt-5' },
  { value: 'claude', label: 'claude-sonnet', description: 'Anthropic' },
]

function Harness({ onChange }: { onChange: (value: string | undefined) => void }) {
  const [value, setValue] = useState<string | undefined>(undefined)
  return (
    <Combobox
      options={options}
      value={value}
      onChange={(next) => {
        setValue(next)
        onChange(next)
      }}
      placeholder="Select a model"
    />
  )
}

describe('Combobox', () => {
  it('renders a combobox trigger with aria-expanded false initially', () => {
    render(<Harness onChange={() => {}} />)
    const trigger = screen.getByRole('combobox')
    expect(trigger).toHaveAttribute('aria-expanded', 'false')
    expect(trigger).toHaveTextContent('Select a model')
  })

  it('opens and lists options', () => {
    render(<Harness onChange={() => {}} />)
    const trigger = screen.getByRole('combobox', { name: '' })
    fireEvent.click(trigger)
    expect(trigger).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByText('gpt-5')).toBeInTheDocument()
    expect(screen.getByText('claude-sonnet')).toBeInTheDocument()
  })

  it('renders each option row with comfortable padding', () => {
    render(<Harness onChange={() => {}} />)
    const trigger = screen.getByRole('combobox', { name: '' })
    fireEvent.click(trigger)
    const option = screen.getByText('gpt-5').closest('[data-slot="command-item"]')
    expect(option).toHaveClass('py-2.5')
  })

  it('selecting an option calls onChange and closes the popover', () => {
    const onChange = vi.fn()
    render(<Harness onChange={onChange} />)
    const trigger = screen.getByRole('combobox', { name: '' })
    fireEvent.click(trigger)
    fireEvent.click(screen.getByText('claude-sonnet'))
    expect(onChange).toHaveBeenCalledWith('claude')
    expect(trigger).toHaveAttribute('aria-expanded', 'false')
  })
})
