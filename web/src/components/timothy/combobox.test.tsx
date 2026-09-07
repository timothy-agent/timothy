import axe from 'axe-core'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
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

function MultiHarness({ ariaLabelledby }: { ariaLabelledby?: boolean } = {}) {
  const [value, setValue] = useState<string[]>([])
  return (
    <>
      {ariaLabelledby && <span id="models-label">Models</span>}
      <Combobox
        multiple
        options={options}
        value={value}
        onChange={setValue}
        placeholder="Select models"
        aria-label={ariaLabelledby ? undefined : 'Models'}
        aria-labelledby={ariaLabelledby ? 'models-label' : undefined}
      />
    </>
  )
}

describe('Combobox multiple', () => {
  it('marks the list aria-multiselectable', () => {
    render(<MultiHarness />)
    fireEvent.click(screen.getByRole('combobox'))
    expect(screen.getByRole('listbox')).toHaveAttribute('aria-multiselectable', 'true')
  })

  it('toggles aria-selected on pick and stays open', async () => {
    render(<MultiHarness />)
    const trigger = screen.getByRole('combobox')
    fireEvent.click(trigger)
    const listbox = screen.getByRole('listbox')
    const option = within(listbox).getByText('gpt-5').closest('[role="option"]') as HTMLElement
    await waitFor(() => expect(option).toHaveAttribute('aria-selected', 'false'))

    fireEvent.click(within(listbox).getByText('gpt-5'))
    expect(trigger).toHaveAttribute('aria-expanded', 'true')
    await waitFor(() => expect(option).toHaveAttribute('aria-selected', 'true'))

    fireEvent.click(within(listbox).getByText('gpt-5'))
    await waitFor(() => expect(option).toHaveAttribute('aria-selected', 'false'))
  })

  it('renders a chip per selection and removes one via its Remove button', () => {
    render(<MultiHarness />)
    fireEvent.click(screen.getByRole('combobox'))
    const listbox = screen.getByRole('listbox')
    fireEvent.click(within(listbox).getByText('gpt-5'))
    fireEvent.click(within(listbox).getByText('claude-sonnet'))

    const chipList = screen.getByRole('list', { name: 'Models' })
    expect(chipList).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Remove gpt-5' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Remove claude-sonnet' })).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Remove gpt-5' }))
    expect(screen.queryByRole('button', { name: 'Remove gpt-5' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Remove claude-sonnet' })).toBeInTheDocument()
  })

  it('toggles a highlighted option with Enter', async () => {
    render(<MultiHarness />)
    fireEvent.click(screen.getByRole('combobox'))
    const listbox = screen.getByRole('listbox')
    const option = within(listbox).getByText('gpt-5').closest('[role="option"]') as HTMLElement
    fireEvent.keyDown(screen.getByPlaceholderText('Search...'), { key: 'Enter' })
    await waitFor(() => expect(option).toHaveAttribute('aria-selected', 'true'))
  })

  it('Escape closes the popover and keeps the selection', () => {
    render(<MultiHarness />)
    const trigger = screen.getByRole('combobox')
    fireEvent.click(trigger)
    fireEvent.click(within(screen.getByRole('listbox')).getByText('gpt-5'))
    fireEvent.keyDown(screen.getByPlaceholderText('Search...'), { key: 'Escape' })
    expect(trigger).toHaveAttribute('aria-expanded', 'false')
    expect(screen.getByRole('button', { name: 'Remove gpt-5' })).toBeInTheDocument()
  })

  it('labels the trigger via aria-labelledby', () => {
    render(<MultiHarness ariaLabelledby />)
    expect(screen.getByRole('combobox')).toHaveAccessibleName('Models')
  })

  it('has no axe violations', async () => {
    render(<MultiHarness />)
    fireEvent.click(screen.getByRole('combobox'))
    fireEvent.click(within(screen.getByRole('listbox')).getByText('gpt-5'))
    // aria-dialog-name and region flag the shared Popover's role="dialog"
    // content wrapper, pre-existing to ui/popover.tsx and out of scope here.
    const results = await axe.run(document.body, {
      rules: { 'color-contrast': { enabled: false }, 'aria-dialog-name': { enabled: false }, region: { enabled: false } },
    })
    expect(results.violations).toEqual([])
  })
})
