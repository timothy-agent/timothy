import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { KbDocument } from '../../api/types'
import { toast } from 'sonner'
import { KbUploadForm, markdownFile, parseUrls } from './KbUploadForm'

vi.mock('../../onboarding/context', async () => {
  const { onboardingState } = await import('../../onboarding/testing')
  return { useOnboarding: () => onboardingState() }
})

vi.mock('sonner', () => ({ toast: { error: vi.fn() } }))

afterEach(cleanup)

const doc: KbDocument = {
  id: 'd1',
  collection_id: 'c1',
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

describe('parseUrls', () => {
  it('keeps only unique valid http(s) URLs in first-seen order', () => {
    expect(parseUrls('https://a.com http://b.com not-a-url https://a.com')).toEqual([
      'https://a.com',
      'http://b.com',
    ])
  })
})

describe('markdownFile', () => {
  it('wraps markdown as a .md file named after the title', async () => {
    const f = markdownFile('  Lab notes  ', '# Hi')
    expect(f.name).toBe('Lab notes.md')
    expect(f.type).toBe('text/markdown')
    expect(await f.text()).toBe('# Hi')
  })

  it('replaces path separators so the server keeps the whole title', () => {
    expect(markdownFile('a/b\\c', 'x').name).toBe('a-b-c.md')
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

  const fillMarkdown = (title: string, body: string) => {
    fireEvent.change(screen.getByPlaceholderText('Markdown title'), { target: { value: title } })
    fireEvent.change(screen.getByPlaceholderText('Paste or write markdown'), { target: { value: body } })
  }

  it('disables Add markdown until both title and markdown are non-blank', () => {
    render(<KbUploadForm uploadFile={vi.fn()} addUrl={vi.fn()} onUploaded={vi.fn()} />)
    const button = screen.getByRole('button', { name: 'Add markdown' })
    expect(button).toBeDisabled()

    fillMarkdown('Notes', '   ')
    expect(button).toBeDisabled()

    fillMarkdown('  ', '# body')
    expect(button).toBeDisabled()

    fillMarkdown('Notes', '# body')
    expect(button).not.toBeDisabled()
  })

  it('uploads pasted markdown as a .md file, reports it, and clears the fields', async () => {
    const uploadFile = vi.fn().mockResolvedValue(doc)
    const onUploaded = vi.fn()
    render(<KbUploadForm uploadFile={uploadFile} addUrl={vi.fn()} onUploaded={onUploaded} />)

    fillMarkdown('Notes', '# body')
    fireEvent.click(screen.getByRole('button', { name: 'Add markdown' }))

    await waitFor(() => expect(onUploaded).toHaveBeenCalledWith(doc))
    const sent = uploadFile.mock.calls[0][0] as File
    expect(sent.name).toBe('Notes.md')
    expect(await sent.text()).toBe('# body')
    expect(screen.getByPlaceholderText('Markdown title')).toHaveValue('')
    expect(screen.getByPlaceholderText('Paste or write markdown')).toHaveValue('')
  })

  it('keeps the markdown and toasts the title when the upload fails', async () => {
    const uploadFile = vi.fn().mockRejectedValue(new Error('boom'))
    const onUploaded = vi.fn()
    render(<KbUploadForm uploadFile={uploadFile} addUrl={vi.fn()} onUploaded={onUploaded} />)

    fillMarkdown('Notes', '# body')
    fireEvent.click(screen.getByRole('button', { name: 'Add markdown' }))

    await waitFor(() => expect(toast.error).toHaveBeenCalledWith('Notes: upload failed', expect.anything()))
    expect(onUploaded).not.toHaveBeenCalled()
    expect(screen.getByPlaceholderText('Paste or write markdown')).toHaveValue('# body')
    expect(screen.getByPlaceholderText('Markdown title')).toHaveValue('Notes')
  })
})
