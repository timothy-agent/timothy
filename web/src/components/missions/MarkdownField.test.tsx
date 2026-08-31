import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { MarkdownField } from './MarkdownField'

afterEach(cleanup)

describe('MarkdownField', () => {
  it('renders a textarea in Write mode', () => {
    render(<MarkdownField value="hello" onChange={vi.fn()} />)
    expect(screen.getByRole('textbox')).toHaveValue('hello')
  })

  it('renders markdown in Preview mode', () => {
    render(<MarkdownField value={'**bold** text and:\n\n- one\n- two'} onChange={vi.fn()} />)
    fireEvent.click(screen.getByRole('button', { name: 'Preview' }))

    expect(screen.getByText('bold').tagName).toBe('STRONG')
    expect(screen.getAllByRole('listitem')).toHaveLength(2)
  })

  it('shows an empty state in Preview when the draft is blank', () => {
    render(<MarkdownField value="" onChange={vi.fn()} />)
    fireEvent.click(screen.getByRole('button', { name: 'Preview' }))

    expect(screen.getByText('Nothing to preview')).toBeInTheDocument()
  })

  it('keeps the draft after toggling back to Write', () => {
    const onChange = vi.fn()
    const { rerender } = render(<MarkdownField value="draft text" onChange={onChange} />)
    fireEvent.click(screen.getByRole('button', { name: 'Preview' }))
    expect(screen.getByText('draft text')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Write' }))
    rerender(<MarkdownField value="draft text" onChange={onChange} />)
    expect(screen.getByRole('textbox')).toHaveValue('draft text')
  })

  it('disables the textarea when disabled is set', () => {
    render(<MarkdownField value="" onChange={vi.fn()} disabled />)
    expect(screen.getByRole('textbox')).toBeDisabled()
  })
})
