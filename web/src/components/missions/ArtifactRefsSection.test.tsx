import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { MediaRef } from '../../api/types'
import { ArtifactRefsSection } from './ArtifactRefsSection'

vi.mock('../../api/client', () => ({
  fetchAttachmentBlob: vi.fn(),
}))

import { fetchAttachmentBlob } from '../../api/client'

afterEach(cleanup)
beforeEach(() => {
  vi.mocked(fetchAttachmentBlob).mockResolvedValue(new Blob(['hello']))
})

describe('ArtifactRefsSection', () => {
  it('renders nothing when there are no refs', () => {
    const { container } = render(<ArtifactRefsSection refs={[]} />)
    expect(container.firstChild).toBeNull()
  })

  it('renders a chip per ref, labeled by name or the mime label', () => {
    const refs: MediaRef[] = [
      { id: 'att-1', mime: 'text/markdown', name: 'report.md' },
      { id: 'att-2', mime: 'application/pdf' },
    ]
    render(<ArtifactRefsSection refs={refs} />)
    expect(screen.getByText('report.md')).toBeInTheDocument()
    expect(screen.getByText('PDF')).toBeInTheDocument()
  })

  it('opens the viewer when a chip is clicked', () => {
    const refs: MediaRef[] = [{ id: 'att-1', mime: 'text/markdown', name: 'report.md' }]
    render(<ArtifactRefsSection refs={refs} />)
    fireEvent.click(screen.getByText('report.md'))
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })
})
