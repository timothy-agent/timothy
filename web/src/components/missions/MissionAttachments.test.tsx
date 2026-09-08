import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { PendingAttachment } from '../Composer'
import { MissionAttachments } from './MissionAttachments'

afterEach(cleanup)

function attachment(mime: string, over: Partial<PendingAttachment> = {}): PendingAttachment {
  return { id: 'a1', mime, previewUrl: '', name: 'file', ...over }
}

function renderChips(attachments: PendingAttachment[]) {
  return render(<MissionAttachments attachments={attachments} onChange={vi.fn()} />)
}

// lucide stamps a `lucide-<kebab-name>` class on every glyph it
// renders, so asserting on it pins which icon a mime maps to. The name
// is the icon's own, not the exported alias: FileAudio re-exports
// file-headphone, so it renders as lucide-file-headphone.
describe('MissionAttachments chip icons', () => {
  it('shows the image glyph for an image attachment', () => {
    const { container } = renderChips([attachment('image/png')])
    expect(container.querySelector('.lucide-image')).toBeInTheDocument()
  })

  it('shows the audio glyph for an audio attachment', () => {
    const { container } = renderChips([attachment('audio/mpeg')])
    expect(container.querySelector('.lucide-file-headphone')).toBeInTheDocument()
  })

  it('shows the text glyph for plain text and markdown', () => {
    const { container } = renderChips([attachment('text/plain')])
    expect(container.querySelector('.lucide-file-text')).toBeInTheDocument()
    cleanup()
    const md = renderChips([attachment('text/markdown')])
    expect(md.container.querySelector('.lucide-file-text')).toBeInTheDocument()
  })

  it('falls back to the generic file glyph for anything else', () => {
    const { container } = renderChips([attachment('application/pdf')])
    expect(container.querySelector('.lucide-file')).toBeInTheDocument()
    expect(container.querySelector('.lucide-file-text')).toBeNull()
  })

  it('picks a distinct icon per chip when several mimes are attached', () => {
    const { container } = renderChips([
      attachment('image/png', { id: 'a1' }),
      attachment('audio/mpeg', { id: 'a2' }),
      attachment('application/pdf', { id: 'a3' }),
    ])
    expect(container.querySelector('.lucide-image')).toBeInTheDocument()
    expect(container.querySelector('.lucide-file-headphone')).toBeInTheDocument()
    expect(container.querySelector('svg.lucide-file')).toBeInTheDocument()
  })

  it('swaps the chip icon for a spinner while the upload is in flight', () => {
    const { container } = renderChips([attachment('image/png', { uploading: true })])
    expect(container.querySelector('.lucide-loader-circle')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Remove file' })).not.toBeInTheDocument()
  })
})
