import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { KbCollection, KbDocument } from '../api/types'
import { Knowledge } from './Knowledge'

vi.mock('../api/client', () => ({
  listKbCollections: vi.fn(),
  getKbCollection: vi.fn(),
  createKbCollection: vi.fn(),
  deleteKbCollection: vi.fn(),
  listKbDocuments: vi.fn(),
  uploadKbDocument: vi.fn(),
  deleteKbDocument: vi.fn(),
  reingestKbDocument: vi.fn(),
}))

import {
  createKbCollection,
  deleteKbDocument,
  getKbCollection,
  listKbCollections,
  listKbDocuments,
  reingestKbDocument,
  uploadKbDocument,
} from '../api/client'

const productDocs: KbCollection = {
  id: 'c1',
  name: 'product-docs',
  description: 'Product documentation for support agents.',
  doc_count: 2,
  chunk_count: 40,
  created_at: '2026-08-01T00:00:00Z',
  updated_at: '2026-08-10T00:00:00Z',
}

const readyDoc: KbDocument = {
  id: 'd1',
  collection_id: 'c1',
  title: 'onboarding.pdf',
  source_type: 'file',
  source_ref: 'onboarding.pdf',
  status: 'ready',
  error: '',
  chunk_count: 20,
  bytes: 204_800,
  ingested_at: '2026-08-10T00:00:00Z',
  created_at: '2026-08-10T00:00:00Z',
}

const failedDoc: KbDocument = {
  id: 'd2',
  collection_id: 'c1',
  title: 'broken.docx',
  source_type: 'file',
  source_ref: 'broken.docx',
  status: 'failed',
  error: 'unsupported encoding',
  chunk_count: 0,
  bytes: 1024,
  ingested_at: null,
  created_at: '2026-08-10T00:00:00Z',
}

function renderPage(entry = '/knowledge') {
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <Routes>
        <Route path="/knowledge/*" element={<Knowledge />} />
      </Routes>
    </MemoryRouter>,
  )
}

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(listKbCollections).mockResolvedValue([productDocs])
})

describe('Knowledge page', () => {
  it('renders the page header and collections with doc and chunk counts', async () => {
    renderPage()
    expect(screen.getByRole('heading', { name: 'Knowledge' })).toBeTruthy()
    expect(await screen.findByText('Collections · 1')).toBeTruthy()
    expect(screen.getByText('product-docs')).toBeTruthy()
    expect(screen.getByText(/2 docs · 40 chunks/)).toBeTruthy()
  })

  it('shows an empty state with an explainer and create button', async () => {
    vi.mocked(listKbCollections).mockResolvedValue([])
    renderPage()
    expect(await screen.findByText(/No collections yet/)).toBeTruthy()
    expect(screen.getAllByRole('button', { name: 'New collection' }).length).toBeGreaterThan(0)
  })

  it('creates a collection and navigates to its detail page', async () => {
    vi.mocked(createKbCollection).mockResolvedValue('c2')
    vi.mocked(getKbCollection).mockResolvedValue({ ...productDocs, id: 'c2', name: 'runbooks' })
    vi.mocked(listKbDocuments).mockResolvedValue([])

    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'New collection' }))

    fireEvent.change(await screen.findByPlaceholderText('product-docs, runbooks…'), {
      target: { value: 'Runbooks' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Create collection' }))

    await waitFor(() =>
      expect(createKbCollection).toHaveBeenCalledWith({ name: 'runbooks', description: '' }),
    )
    expect(await screen.findByRole('heading', { name: 'runbooks' })).toBeTruthy()
  })

  describe('detail', () => {
    beforeEach(() => {
      vi.mocked(getKbCollection).mockResolvedValue(productDocs)
      vi.mocked(listKbDocuments).mockResolvedValue([readyDoc, failedDoc])
    })

    it('a deep link renders documents with status badges', async () => {
      renderPage('/knowledge/c1')
      expect(await screen.findByText('onboarding.pdf')).toBeTruthy()
      expect(screen.getByText('ready')).toBeTruthy()
      expect(screen.getByText('failed')).toBeTruthy()
      expect(screen.getByText('broken.docx')).toBeTruthy()
    })

    it('shows the error as a tooltip on a failed document', async () => {
      renderPage('/knowledge/c1')
      await screen.findByText('broken.docx')
      expect(screen.getByText('failed').closest('span')).toHaveAttribute('title', 'unsupported encoding')
    })

    it('uploads a file via the input and adds it to the document list', async () => {
      const uploaded: KbDocument = { ...readyDoc, id: 'd3', title: 'new.md', status: 'pending' }
      vi.mocked(uploadKbDocument).mockResolvedValue(uploaded)

      renderPage('/knowledge/c1')
      await screen.findByText('onboarding.pdf')

      const file = new File(['# hi'], 'new.md', { type: 'text/markdown' })
      const input = document.querySelector('input[type="file"]') as HTMLInputElement
      fireEvent.change(input, { target: { files: [file] } })

      await waitFor(() => expect(uploadKbDocument).toHaveBeenCalledWith('c1', file))
      expect(await screen.findByText('new.md')).toBeTruthy()
    })

    it('re-ingests a failed document', async () => {
      vi.mocked(reingestKbDocument).mockResolvedValue()
      renderPage('/knowledge/c1')
      await screen.findByText('broken.docx')

      fireEvent.click(screen.getByRole('button', { name: 'Re-ingest broken.docx' }))
      await waitFor(() => expect(reingestKbDocument).toHaveBeenCalledWith('d2'))
    })

    it('deletes a document after confirmation', async () => {
      vi.mocked(deleteKbDocument).mockResolvedValue()
      renderPage('/knowledge/c1')
      await screen.findByText('onboarding.pdf')

      fireEvent.click(screen.getByRole('button', { name: 'Delete onboarding.pdf' }))
      fireEvent.click(await screen.findByRole('button', { name: 'Delete' }))

      await waitFor(() => expect(deleteKbDocument).toHaveBeenCalledWith('d1'))
    })
  })
})
