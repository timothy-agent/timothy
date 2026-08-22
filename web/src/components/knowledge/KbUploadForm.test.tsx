import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { KbDocument } from '../../api/types'
import { KbUploadForm, parseUrls } from './KbUploadForm'

afterEach(cleanup)

const doc: KbDocument = {
  id: 'd1',
  collection_id: 'c1',
  title: 'notes',
  source_type: 'file',
  source_ref: 'notes.md',
  status: 'pending',
  error: '',
  chunk_count: 0,
  bytes: 12,
  ingested_at: null,
  created_at: '2026-07-01T00:00:00Z',
}

describe('parseUrls', () => {
  it('keeps only unique valid http(s) URLs in first-seen order', () => {
    expect(parseUrls('https://a.com http://b.com not-a-url https://a.com')).toEqual([
      'https://a.com',
      'http://b.com',
    ])
  })
})

describe('KbUploadForm', () => {
  it('calls uploadFile for a chosen file and reports the created document', async () => {
    const uploadFile = vi.fn().mockResolvedValue(doc)
    const addUrl = vi.fn()
    const onUploaded = vi.fn()
    render(<KbUploadForm uploadFile={uploadFile} addUrl={addUrl} onUploaded={onUploaded} />)

    const file = new File(['content'], 'notes.md', { type: 'text/markdown' })
    const input = document.querySelector('input[type="file"]') as HTMLInputElement
    fireEvent.change(input, { target: { files: [file] } })

    await waitFor(() => expect(uploadFile).toHaveBeenCalledWith(file))
    await waitFor(() => expect(onUploaded).toHaveBeenCalledWith(doc))
    expect(addUrl).not.toHaveBeenCalled()
  })

  it('submits each parsed URL and reports each created document', async () => {
    const uploadFile = vi.fn()
    const addUrl = vi.fn().mockResolvedValue(doc)
    const onUploaded = vi.fn()
    render(<KbUploadForm uploadFile={uploadFile} addUrl={addUrl} onUploaded={onUploaded} />)

    fireEvent.change(screen.getByPlaceholderText(/add a page or PDF by URL/), {
      target: { value: 'https://example.com/a' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Add URL' }))

    await waitFor(() => expect(addUrl).toHaveBeenCalledWith('https://example.com/a'))
    await waitFor(() => expect(onUploaded).toHaveBeenCalledWith(doc))
    expect(uploadFile).not.toHaveBeenCalled()
  })

  it('disables Add URL until the textarea has a valid URL', () => {
    render(<KbUploadForm uploadFile={vi.fn()} addUrl={vi.fn()} onUploaded={vi.fn()} />)
    expect(screen.getByRole('button', { name: 'Add URL' })).toBeDisabled()

    fireEvent.change(screen.getByPlaceholderText(/add a page or PDF by URL/), {
      target: { value: 'https://example.com/a' },
    })
    expect(screen.getByRole('button', { name: 'Add URL' })).not.toBeDisabled()
  })
})
