import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { CatalogModel } from '../../api/types'
import { catalogMatchForID, ModelInput } from './ModelInput'

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

  it('shows a compact price label for a priced suggestion', () => {
    render(
      <ModelInput
        value=""
        onChange={vi.fn()}
        suggestions={[{ id: 'gpt-5.6-sol', input_per_mtok: 1.25, output_per_mtok: 10 }]}
      />,
    )
    fireEvent.focus(screen.getByRole('textbox'))
    expect(screen.getByText('in $1.25 · out $10 /MTok')).toBeInTheDocument()
  })

  it('shows "unpriced" when a suggestion has no price', () => {
    render(<ModelInput value="" onChange={vi.fn()} suggestions={[{ id: 'gpt-5.6-sol' }]} />)
    fireEvent.focus(screen.getByRole('textbox'))
    expect(screen.getByText('unpriced')).toBeInTheDocument()
  })

  it('shows "free" when both prices are explicitly zero (a genuinely free model)', () => {
    render(
      <ModelInput
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
      <ModelInput
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
      <ModelInput
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
