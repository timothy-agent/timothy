import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { InputRequestBanner } from './InputRequestBanner'

afterEach(cleanup)

describe('InputRequestBanner', () => {
  it('renders the question as markdown', () => {
    render(
      <InputRequestBanner
        question="which **runtime** should this target?"
        kind="mcq"
        options={['node', 'python']}
        proposedDefault="node"
        onAnswer={vi.fn()}
      />,
    )
    expect(screen.getByText('runtime').tagName).toBe('STRONG')
  })

  it('renders mcq options with the proposed default marked, and answers on click', () => {
    const onAnswer = vi.fn()
    render(
      <InputRequestBanner
        question="which runtime should this target?"
        kind="mcq"
        options={['node', 'python']}
        proposedDefault="node"
        onAnswer={onAnswer}
      />,
    )
    expect(screen.getByText(/which runtime should this target/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /node.*default/ })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /^python$/ }))
    expect(onAnswer).toHaveBeenCalledWith('python')
  })

  it('renders yes/no buttons with the proposed default marked', () => {
    const onAnswer = vi.fn()
    render(
      <InputRequestBanner
        question="continue?"
        kind="yes_no"
        proposedDefault="yes"
        onAnswer={onAnswer}
      />,
    )
    expect(screen.getByRole('button', { name: /Yes.*default/ })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /^No$/ }))
    expect(onAnswer).toHaveBeenCalledWith('no')
  })

  it('renders an open MarkdownField and submits the drafted text unchanged', () => {
    const onAnswer = vi.fn()
    render(
      <InputRequestBanner
        question="what should the title be?"
        kind="open"
        proposedDefault="Untitled"
        onAnswer={onAnswer}
      />,
    )
    const textarea = screen.getByRole('textbox')
    fireEvent.change(textarea, { target: { value: 'My **Report**' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    expect(onAnswer).toHaveBeenCalledWith('My **Report**')
  })

  it('falls back to the proposed default when open text is left empty', () => {
    const onAnswer = vi.fn()
    render(
      <InputRequestBanner
        question="what should the title be?"
        kind="open"
        proposedDefault="Untitled"
        onAnswer={onAnswer}
      />,
    )
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    expect(onAnswer).toHaveBeenCalledWith('Untitled')
  })

  it('shows a status line and hides the inputs once answered', () => {
    render(
      <InputRequestBanner
        question="continue?"
        kind="yes_no"
        proposedDefault="yes"
        answered="yes"
        onAnswer={vi.fn()}
      />,
    )
    expect(screen.getByText(/Answered: yes/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^Yes/ })).not.toBeInTheDocument()
  })

  it('always names the proposed default, adding the timeout only when both are given', () => {
    const { rerender } = render(
      <InputRequestBanner
        question="continue?"
        kind="yes_no"
        proposedDefault="yes"
        onAnswer={vi.fn()}
      />,
    )
    expect(screen.getByText(/Auto-answers with the proposed default \(Yes\)/)).toBeInTheDocument()
    expect(screen.queryByText(/if unanswered for/)).not.toBeInTheDocument()

    rerender(
      <InputRequestBanner
        question="continue?"
        kind="yes_no"
        proposedDefault="yes"
        onAnswer={vi.fn()}
        timeoutSeconds={300}
        askedAt="2026-08-30T00:00:00Z"
      />,
    )
    expect(screen.getByText(/if unanswered for 300s/)).toBeInTheDocument()
  })
})
