import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { KbCollection } from '../../api/types'
import { KnowledgePicker } from './KnowledgePicker'

vi.mock('../../api/client', () => ({
  listKbCollections: vi.fn(),
}))

import { listKbCollections } from '../../api/client'

const collections: KbCollection[] = [
  {
    id: 'c1',
    name: 'product-docs',
    description: 'Product documentation.',
    doc_count: 1,
    chunk_count: 5,
    failed_count: 0,
    retrieval_weight: 1.0,
    created_at: '2026-08-01T00:00:00Z',
    updated_at: '2026-08-01T00:00:00Z',
  },
]

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
  // jsdom lacks scrollIntoView; cmdk calls it when an item mounts.
  Element.prototype.scrollIntoView = vi.fn()
})

describe('KnowledgePicker', () => {
  it('shows "No knowledge" when nothing is selected', async () => {
    vi.mocked(listKbCollections).mockResolvedValue(collections)
    render(<KnowledgePicker value={[]} onChange={vi.fn()} />)
    expect(await screen.findByText('No knowledge')).toBeTruthy()
  })

  it('selects a collection from the popover', async () => {
    vi.mocked(listKbCollections).mockResolvedValue(collections)
    const onChange = vi.fn()
    render(<KnowledgePicker value={[]} onChange={onChange} />)

    fireEvent.click(await screen.findByRole('combobox'))
    fireEvent.click(await screen.findByText('product-docs'))

    expect(onChange).toHaveBeenCalledWith(['product-docs'])
  })

  it('removes a selected collection via its chip', async () => {
    vi.mocked(listKbCollections).mockResolvedValue(collections)
    const onChange = vi.fn()
    render(<KnowledgePicker value={['product-docs']} onChange={onChange} />)

    fireEvent.click(await screen.findByLabelText('Remove product-docs'))
    expect(onChange).toHaveBeenCalledWith([])
  })

  it('falls back to free text when the collection list is empty', async () => {
    vi.mocked(listKbCollections).mockResolvedValue([])
    const onChange = vi.fn()
    render(<KnowledgePicker value={[]} onChange={onChange} />)

    const input = await screen.findByPlaceholderText('product-docs, runbooks')
    fireEvent.change(input, { target: { value: 'a, b' } })
    expect(onChange).toHaveBeenCalledWith(['a', 'b'])
  })
})
