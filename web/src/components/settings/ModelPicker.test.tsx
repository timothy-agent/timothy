import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { CatalogModel } from '../../api/types'
import { catalogMatchForID, matchSuggestion, ModelPicker } from './ModelPicker'

afterEach(cleanup)

describe('catalogMatchForID', () => {
  const pool: CatalogModel[] = [
    { id: 'grok-2', model_key: 'xai/grok-2', litellm_provider: 'xai', mode: 'chat' },
    { id: 'glm-4.7-flash', model_key: 'zai/glm-4.7-flash', litellm_provider: 'zai', mode: 'chat' },
    { id: 'gpt-4o', model_key: 'gpt-4o', litellm_provider: 'openai', mode: 'chat' },
  ]

  it('matches the exact model_key first', () => {
    expect(catalogMatchForID('gpt-4o', pool)?.model_key).toBe('gpt-4o')
  })

  it('falls back to the segment after the last slash', () => {
    expect(catalogMatchForID('grok-2', pool)?.model_key).toBe('xai/grok-2')
    expect(catalogMatchForID('glm-4.7-flash', pool)?.model_key).toBe('zai/glm-4.7-flash')
  })

  it('returns undefined when nothing matches', () => {
    expect(catalogMatchForID('unknown-model', pool)).toBeUndefined()
  })
})

describe('matchSuggestion', () => {
  const suggestions = [{ id: 'GPT-4o' }, { id: 'claude-haiku-4-5' }]

  it('matches a suggestion case-insensitively', () => {
    expect(matchSuggestion('gpt-4o', suggestions)?.id).toBe('GPT-4o')
  })

  it('returns undefined when no suggestion matches', () => {
    expect(matchSuggestion('unknown', suggestions)).toBeUndefined()
  })
})

describe('ModelPicker', () => {
  it('shows the friendly name primary and the raw id secondary', () => {
    render(
      <ModelPicker
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
    render(<ModelPicker value="" onChange={vi.fn()} suggestions={[{ id: 'gpt-5.6-sol' }]} />)
    fireEvent.focus(screen.getByRole('textbox'))
    expect(screen.getByText('gpt-5.6-sol')).toBeInTheDocument()
  })

  it('filters suggestions by name as well as id', () => {
    render(
      <ModelPicker
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

  it('shows a compact price label for a priced suggestion', () => {
    render(
      <ModelPicker
        value=""
        onChange={vi.fn()}
        suggestions={[{ id: 'gpt-5.6-sol', input_per_mtok: 1.25, output_per_mtok: 10 }]}
      />,
    )
    fireEvent.focus(screen.getByRole('textbox'))
    expect(screen.getByText('in $1.25 · out $10 /MTok')).toBeInTheDocument()
  })

  it('shows "unpriced" when a suggestion has no price', () => {
    render(<ModelPicker value="" onChange={vi.fn()} suggestions={[{ id: 'gpt-5.6-sol' }]} />)
    fireEvent.focus(screen.getByRole('textbox'))
    expect(screen.getByText('unpriced')).toBeInTheDocument()
  })

  it('shows "free" when both prices are explicitly zero (a genuinely free model)', () => {
    render(
      <ModelPicker
        value=""
        onChange={vi.fn()}
        suggestions={[{ id: 'gpt-5.6-sol', input_per_mtok: 0, output_per_mtok: 0 }]}
      />,
    )
    fireEvent.focus(screen.getByRole('textbox'))
    expect(screen.getByText('free')).toBeInTheDocument()
  })

  it('shows N/A for the missing side when only one price is known', () => {
    render(
      <ModelPicker
        value=""
        onChange={vi.fn()}
        suggestions={[{ id: 'gpt-5.6-sol', input_per_mtok: 0.13, output_per_mtok: undefined }]}
      />,
    )
    fireEvent.focus(screen.getByRole('textbox'))
    expect(screen.getByText('in $0.13 · out N/A /MTok')).toBeInTheDocument()
  })

  it('renders an explicit zero side as $0, not N/A', () => {
    render(
      <ModelPicker
        value=""
        onChange={vi.fn()}
        suggestions={[{ id: 'gpt-5.6-sol', input_per_mtok: 0, output_per_mtok: 15 }]}
      />,
    )
    fireEvent.focus(screen.getByRole('textbox'))
    expect(screen.getByText('in $0 · out $15 /MTok')).toBeInTheDocument()
  })

  it('picking a suggestion submits the id, not the name', () => {
    const onChange = vi.fn()
    render(
      <ModelPicker
        value=""
        onChange={onChange}
        suggestions={[{ id: 'amazon.nova-lite-v1:0', name: 'Nova Lite' }]}
      />,
    )
    fireEvent.focus(screen.getByRole('textbox'))
    fireEvent.click(screen.getByText('Nova Lite'))
    expect(onChange).toHaveBeenCalledWith('amazon.nova-lite-v1:0')
  })

  it('ArrowDown then Enter picks the highlighted suggestion', () => {
    const onChange = vi.fn()
    render(
      <ModelPicker
        value=""
        onChange={onChange}
        suggestions={[{ id: 'model-a' }, { id: 'model-b' }]}
      />,
    )
    const input = screen.getByRole('textbox')
    fireEvent.focus(input)
    fireEvent.keyDown(input, { key: 'ArrowDown' })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(onChange).toHaveBeenCalledWith('model-a')
  })

  it('ArrowDown twice then Enter picks the second suggestion', () => {
    const onChange = vi.fn()
    render(
      <ModelPicker
        value=""
        onChange={onChange}
        suggestions={[{ id: 'model-a' }, { id: 'model-b' }]}
      />,
    )
    const input = screen.getByRole('textbox')
    fireEvent.focus(input)
    fireEvent.keyDown(input, { key: 'ArrowDown' })
    fireEvent.keyDown(input, { key: 'ArrowDown' })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(onChange).toHaveBeenCalledWith('model-b')
  })

  it('ArrowUp from the top wraps around to the last suggestion', () => {
    const onChange = vi.fn()
    render(
      <ModelPicker
        value=""
        onChange={onChange}
        suggestions={[{ id: 'model-a' }, { id: 'model-b' }]}
      />,
    )
    const input = screen.getByRole('textbox')
    fireEvent.focus(input)
    fireEvent.keyDown(input, { key: 'ArrowUp' })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(onChange).toHaveBeenCalledWith('model-b')
  })

  it('ArrowDown then ArrowUp moves highlight back to the previous suggestion', () => {
    const onChange = vi.fn()
    render(
      <ModelPicker
        value=""
        onChange={onChange}
        suggestions={[{ id: 'model-a' }, { id: 'model-b' }]}
      />,
    )
    const input = screen.getByRole('textbox')
    fireEvent.focus(input)
    fireEvent.keyDown(input, { key: 'ArrowDown' })
    fireEvent.keyDown(input, { key: 'ArrowDown' })
    fireEvent.keyDown(input, { key: 'ArrowUp' })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(onChange).toHaveBeenCalledWith('model-a')
  })

  it('Escape closes the popover and keeps the typed text', () => {
    const onChange = vi.fn()
    render(
      <ModelPicker
        value="model"
        onChange={onChange}
        suggestions={[{ id: 'model-a' }]}
      />,
    )
    const input = screen.getByRole('textbox') as HTMLInputElement
    fireEvent.focus(input)
    expect(screen.getByRole('option', { name: /model-a/ })).toBeInTheDocument()
    fireEvent.keyDown(input, { key: 'Escape' })
    expect(screen.queryByRole('option', { name: /model-a/ })).not.toBeInTheDocument()
    expect(input.value).toBe('model')
    expect(onChange).not.toHaveBeenCalled()
  })
})
