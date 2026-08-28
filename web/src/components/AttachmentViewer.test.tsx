import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AttachmentViewer } from './AttachmentViewer'

vi.mock('../api/client', () => ({
  fetchAttachmentBlob: vi.fn(),
}))

import { fetchAttachmentBlob } from '../api/client'

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
  URL.createObjectURL = vi.fn(() => 'blob:mock')
  URL.revokeObjectURL = vi.fn()
})

describe('AttachmentViewer', () => {
  it('renders nothing when no attachment is given', () => {
    render(<AttachmentViewer open={false} onOpenChange={() => {}} attachment={null} />)
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('renders an image lightbox for image/*', async () => {
    vi.mocked(fetchAttachmentBlob).mockResolvedValue(new Blob(['x'], { type: 'image/png' }))
    render(
      <AttachmentViewer
        open
        onOpenChange={() => {}}
        attachment={{ id: 'img1', mime: 'image/png', name: 'photo.png' }}
      />,
    )
    const img = await screen.findByRole('img', { name: 'photo.png' })
    expect(img.getAttribute('src')).toBe('blob:mock')
  })

  it('renders a video element with controls for video/*', async () => {
    vi.mocked(fetchAttachmentBlob).mockResolvedValue(new Blob(['x'], { type: 'video/mp4' }))
    render(
      <AttachmentViewer
        open
        onOpenChange={() => {}}
        attachment={{ id: 'vid1', mime: 'video/mp4', name: 'clip.mp4' }}
      />,
    )
    // Dialog content portals to document.body, not the render container.
    const video = await waitFor(() => {
      const el = document.body.querySelector('video')
      if (!el) throw new Error('video not rendered yet')
      return el
    })
    expect(video.getAttribute('src')).toBe('blob:mock')
    expect(video.hasAttribute('controls')).toBe(true)
  })

  it('renders an audio element with controls for audio/*', async () => {
    vi.mocked(fetchAttachmentBlob).mockResolvedValue(new Blob(['x'], { type: 'audio/mpeg' }))
    render(
      <AttachmentViewer
        open
        onOpenChange={() => {}}
        attachment={{ id: 'aud1', mime: 'audio/mpeg', name: 'note.mp3' }}
      />,
    )
    const audio = await waitFor(() => {
      const el = document.body.querySelector('audio')
      if (!el) throw new Error('audio not rendered yet')
      return el
    })
    expect(audio.getAttribute('src')).toBe('blob:mock')
    expect(audio.hasAttribute('controls')).toBe(true)
  })

  it('renders a full-size iframe for application/pdf', async () => {
    vi.mocked(fetchAttachmentBlob).mockResolvedValue(new Blob(['x'], { type: 'application/pdf' }))
    render(
      <AttachmentViewer
        open
        onOpenChange={() => {}}
        attachment={{ id: 'doc1', mime: 'application/pdf', name: 'report.pdf' }}
      />,
    )
    const frame = await screen.findByTitle('report.pdf')
    expect(frame.tagName).toBe('IFRAME')
    expect(frame.getAttribute('src')).toBe('blob:mock')
  })

  it('renders markdown for text/markdown with a Source/Rendered toggle', async () => {
    vi.mocked(fetchAttachmentBlob).mockResolvedValue(new Blob(['# Hello'], { type: 'text/markdown' }))
    render(
      <AttachmentViewer
        open
        onOpenChange={() => {}}
        attachment={{ id: 'md1', mime: 'text/markdown', name: 'notes.md' }}
      />,
    )
    expect(await screen.findByRole('heading', { name: 'Hello' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Source' })).toBeInTheDocument()
  })

  it('renders plain text for text/plain with a Copy button, no Source toggle', async () => {
    vi.mocked(fetchAttachmentBlob).mockResolvedValue(new Blob(['plain body'], { type: 'text/plain' }))
    render(
      <AttachmentViewer
        open
        onOpenChange={() => {}}
        attachment={{ id: 'txt1', mime: 'text/plain', name: 'notes.txt' }}
      />,
    )
    expect(await screen.findByText('plain body')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Copy' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Source' })).not.toBeInTheDocument()
  })

  it('falls back to a type label + short hash when no name is present', async () => {
    vi.mocked(fetchAttachmentBlob).mockResolvedValue(new Blob(['x'], { type: 'application/pdf' }))
    render(
      <AttachmentViewer
        open
        onOpenChange={() => {}}
        attachment={{ id: 'abcdefgh12345678', mime: 'application/pdf' }}
      />,
    )
    expect(await screen.findByText('PDF (abcdefgh)')).toBeInTheDocument()
  })

  it('renders exactly one close control, in the header row, not the default overlapping one', async () => {
    vi.mocked(fetchAttachmentBlob).mockResolvedValue(new Blob(['x'], { type: 'application/pdf' }))
    render(
      <AttachmentViewer
        open
        onOpenChange={() => {}}
        attachment={{ id: 'doc1', mime: 'application/pdf', name: 'report.pdf' }}
      />,
    )
    await screen.findByText('Download')
    expect(screen.getAllByRole('button', { name: 'Close' })).toHaveLength(1)
  })

  it('uses the given localUrl directly without fetching', () => {
    render(
      <AttachmentViewer
        open
        onOpenChange={() => {}}
        attachment={{ id: 'img1', mime: 'image/png', name: 'photo.png' }}
        localUrl="blob:local-preview"
      />,
    )
    expect(fetchAttachmentBlob).not.toHaveBeenCalled()
    const img = screen.getByRole('img', { name: 'photo.png' }) as HTMLImageElement
    expect(img.src).toBe('blob:local-preview')
  })
})
