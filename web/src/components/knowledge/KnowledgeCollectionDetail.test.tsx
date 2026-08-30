import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import type { KbDocument } from '../../api/types'
import { TooltipProvider } from '../ui/tooltip'
import { DocumentErrorLine, ProvenanceBadge, SourceBadge } from './KnowledgeCollectionDetail'

afterEach(cleanup)

const baseDoc: KbDocument = {
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

function renderBadge(doc: KbDocument) {
  return render(
    <TooltipProvider>
      <SourceBadge doc={doc} />
    </TooltipProvider>,
  )
}

describe('SourceBadge', () => {
  it('links a url document to its source_ref in a new tab', () => {
    const doc: KbDocument = { ...baseDoc, source_type: 'url', source_ref: 'https://example.com/a' }
    renderBadge(doc)
    const link = screen.getByRole('link')
    expect(link).toHaveAttribute('href', 'https://example.com/a')
    expect(link).toHaveAttribute('target', '_blank')
    expect(link).toHaveAttribute('rel', 'noopener noreferrer')
  })

  it('links a clip document to its source_ref in a new tab', () => {
    const doc: KbDocument = { ...baseDoc, source_type: 'clip', source_ref: 'https://example.com/b' }
    renderBadge(doc)
    const link = screen.getByRole('link')
    expect(link).toHaveAttribute('href', 'https://example.com/b')
    expect(link).toHaveAttribute('target', '_blank')
    expect(link).toHaveAttribute('rel', 'noopener noreferrer')
  })

  it('renders a file document as a plain badge with no link', () => {
    renderBadge(baseDoc)
    expect(screen.queryByRole('link')).not.toBeInTheDocument()
    expect(screen.getByText('file')).toBeInTheDocument()
  })
})

describe('ProvenanceBadge', () => {
  afterEach(cleanup)

  it('shows the provenance tier label', () => {
    render(<ProvenanceBadge doc={{ ...baseDoc, provenance: 'mission' }} />)
    expect(screen.getByText('Mission')).toBeInTheDocument()
  })
})

describe('DocumentErrorLine', () => {
  afterEach(cleanup)

  it('renders the failure reason for a failed document with an error', () => {
    const doc: KbDocument = { ...baseDoc, status: 'failed', error: 'chain_exhausted: too many input tokens' }
    render(<DocumentErrorLine doc={doc} />)
    const line = screen.getByText('chain_exhausted: too many input tokens')
    expect(line).toBeInTheDocument()
    expect(line).toHaveAttribute('title', doc.error)
    expect(line).toHaveClass('text-red-400')
  })

  it('renders nothing for a ready document', () => {
    const doc: KbDocument = { ...baseDoc, status: 'ready', error: '' }
    const { container } = render(<DocumentErrorLine doc={doc} />)
    expect(container).toBeEmptyDOMElement()
  })

  it('renders nothing for a failed document with no error text', () => {
    const doc: KbDocument = { ...baseDoc, status: 'failed', error: '' }
    const { container } = render(<DocumentErrorLine doc={doc} />)
    expect(container).toBeEmptyDOMElement()
  })
})
