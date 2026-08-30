import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { KbDocument } from '../../api/types'
import { KnowledgeAutoAdd } from './KnowledgeAutoAdd'

vi.mock('../../api/client', () => ({
  uploadKbDocumentAuto: vi.fn(),
  addKbDocumentFromUrlAuto: vi.fn(),
}))

import { addKbDocumentFromUrlAuto, uploadKbDocumentAuto } from '../../api/client'

const doc: KbDocument = {
  id: 'd1',
  collection_id: 'c-classified',
  title: 'notes',
  source_type: 'file',
  source_ref: 'notes.md',
  provenance: 'curated',
  status: 'pending',
  error: '',
  chunk_count: 0,
  bytes: 12,
  ingested_at: null,
  created_at: '2026-07-01T00:00:00Z',
}

function renderPage() {
  const router = createMemoryRouter(
    [
      { path: '/knowledge/add', element: <KnowledgeAutoAdd /> },
      { path: '/knowledge/:id', element: <div>collection detail page</div> },
    ],
    { initialEntries: ['/knowledge/add'] },
  )
  return render(<RouterProvider router={router} />)
}

afterEach(cleanup)
beforeEach(() => vi.clearAllMocks())

describe('KnowledgeAutoAdd', () => {
  it('uploads a file with no collection chosen and navigates to the classified collection', async () => {
    vi.mocked(uploadKbDocumentAuto).mockResolvedValue(doc)
    renderPage()

    const file = new File(['content'], 'notes.md', { type: 'text/markdown' })
    const input = document.querySelector('input[type="file"]') as HTMLInputElement
    fireEvent.change(input, { target: { files: [file] } })

    await waitFor(() => expect(uploadKbDocumentAuto).toHaveBeenCalledWith(file))
    expect(await screen.findByText('collection detail page')).toBeTruthy()
    expect(addKbDocumentFromUrlAuto).not.toHaveBeenCalled()
  })

  it('adds a URL with no collection chosen and navigates to the classified collection', async () => {
    vi.mocked(addKbDocumentFromUrlAuto).mockResolvedValue(doc)
    renderPage()

    fireEvent.change(screen.getByPlaceholderText(/add a page or PDF by URL/), {
      target: { value: 'https://example.com/article' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Add URL' }))

    await waitFor(() => expect(addKbDocumentFromUrlAuto).toHaveBeenCalledWith('https://example.com/article'))
    expect(await screen.findByText('collection detail page')).toBeTruthy()
  })
})
