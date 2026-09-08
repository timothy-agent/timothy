import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { MediaRef, MissionFile } from '../../api/types'
import { ArtifactsSection } from './ArtifactsSection'

vi.mock('../../api/client', () => ({
  listMissionFiles: vi.fn(),
  downloadMissionFile: vi.fn(),
  downloadMissionArchive: vi.fn(),
  downloadMissionPdfExport: vi.fn(),
  exportMissionPdf: vi.fn(),
  getSettings: vi.fn(),
  fetchMissionFileBlob: vi.fn(),
  fetchAttachmentBlob: vi.fn(),
  listKbCollections: vi.fn(),
  promoteMissionToKB: vi.fn(),
  missionFilePreviewCap: 1_000_000,
  MissionFileTooLargeError: class MissionFileTooLargeError extends Error {},
}))

import {
  downloadMissionPdfExport,
  exportMissionPdf,
  fetchAttachmentBlob,
  fetchMissionFileBlob,
  getSettings,
  listKbCollections,
  listMissionFiles,
  promoteMissionToKB,
} from '../../api/client'

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
  // jsdom lacks scrollIntoView; Radix Select calls it on open.
  Element.prototype.scrollIntoView = vi.fn()
  vi.mocked(listMissionFiles).mockResolvedValue({ files: [], truncated: false })
  vi.mocked(fetchMissionFileBlob).mockResolvedValue(new Blob(['hello']))
  vi.mocked(fetchAttachmentBlob).mockResolvedValue(new Blob(['hello']))
  vi.mocked(getSettings).mockResolvedValue({ settings: {}, values: {} })
  vi.mocked(listKbCollections).mockResolvedValue([])
})

const refs: MediaRef[] = [{ id: 'att-1', mime: 'text/markdown', name: 'report.md' }]

const files: MissionFile[] = [
  { path: 'src/a.txt', size: 512, mtime: '2026-01-01T00:00:00Z', declared: false },
  { path: 'out/report.json', size: 2048, mtime: '2026-01-01T00:00:00Z', declared: true },
  { path: 'big.bin', size: 1_572_864, mtime: '2026-01-01T00:00:00Z', declared: false },
]

describe('ArtifactsSection', () => {
  it('renders nothing when there is no workspace', () => {
    const { container } = render(
      <ArtifactsSection missionId="m1" phase="execute" workspace={undefined} />,
    )
    expect(container.firstChild).toBeNull()
    expect(listMissionFiles).not.toHaveBeenCalled()
  })

  it('renders a folder tree with declared badges only on declared files', async () => {
    vi.mocked(listMissionFiles).mockResolvedValue({ files, truncated: false })
    render(<ArtifactsSection missionId="m1" phase="execute" workspace="ws-1" />)

    expect(await screen.findByText('src')).toBeTruthy()
    expect(screen.getByText('out')).toBeTruthy()
    expect(screen.getByText('a.txt')).toBeTruthy()
    expect(screen.getByText('report.json')).toBeTruthy()
    expect(screen.getByText('big.bin')).toBeTruthy()

    // Only the declared file gets the badge.
    expect(screen.getAllByText('declared')).toHaveLength(1)
  })

  it('shows a viewer with the file path and size once a file is selected', async () => {
    vi.mocked(listMissionFiles).mockResolvedValue({ files, truncated: false })
    render(<ArtifactsSection missionId="m1" phase="execute" workspace="ws-1" />)

    fireEvent.click(await screen.findByText('a.txt'))
    expect(await screen.findByText('src/a.txt')).toBeTruthy()
    expect(screen.getByText('512 B')).toBeTruthy()
  })

  it('marks the selected tree row with aria-current', async () => {
    vi.mocked(listMissionFiles).mockResolvedValue({ files, truncated: false })
    render(<ArtifactsSection missionId="m1" phase="execute" workspace="ws-1" />)

    const row = await screen.findByText('a.txt')
    fireEvent.click(row)
    expect(row.closest('button')).toHaveAttribute('aria-current', 'true')
  })

  it('renders nothing while the workspace has no files', async () => {
    const { container } = render(
      <ArtifactsSection missionId="m1" phase="execute" workspace="ws-1" />,
    )
    await waitFor(() => expect(vi.mocked(listMissionFiles)).toHaveBeenCalled())
    expect(container).toBeEmptyDOMElement()
  })

  it('shows the section with Download all enabled once files exist', async () => {
    vi.mocked(listMissionFiles).mockResolvedValue({ files, truncated: false })
    render(<ArtifactsSection missionId="m1" phase="execute" workspace="ws-1" />)
    await screen.findByText('big.bin')
    expect(screen.getByText('Files')).toBeTruthy()
    expect(
      screen.getByRole('button', { name: 'Download the workspace as a zip archive' }),
    ).not.toBeDisabled()
  })

  it('shows a truncated hint only when the listing was truncated', async () => {
    vi.mocked(listMissionFiles).mockResolvedValue({ files, truncated: true })
    render(<ArtifactsSection missionId="m1" phase="execute" workspace="ws-1" />)
    expect(await screen.findByText('list truncated')).toBeTruthy()
  })

  it('omits the truncated hint when the listing was not truncated', async () => {
    vi.mocked(listMissionFiles).mockResolvedValue({ files, truncated: false })
    render(<ArtifactsSection missionId="m1" phase="execute" workspace="ws-1" />)
    await screen.findByText('big.bin')
    expect(screen.queryByText('list truncated')).toBeNull()
  })

  it('opens a fullscreen dialog preserving tree selection, and closes on exit', async () => {
    vi.mocked(listMissionFiles).mockResolvedValue({ files, truncated: false })
    render(<ArtifactsSection missionId="m1" phase="execute" workspace="ws-1" />)

    fireEvent.click(await screen.findByText('a.txt'))
    await screen.findByText('src/a.txt')

    fireEvent.click(screen.getByRole('button', { name: 'Fullscreen' }))
    expect(screen.getByRole('dialog')).toBeTruthy()
    expect(screen.getByText('src/a.txt')).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: 'Exit fullscreen' }))
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('closes the fullscreen dialog on Escape', async () => {
    vi.mocked(listMissionFiles).mockResolvedValue({ files, truncated: false })
    render(<ArtifactsSection missionId="m1" phase="execute" workspace="ws-1" />)
    await screen.findByText('big.bin')

    fireEvent.click(screen.getByRole('button', { name: 'Fullscreen' }))
    const dialog = screen.getByRole('dialog')
    // The click also focused the button, opening its Radix tooltip as
    // its own dismissable layer above the dialog: the first Escape
    // dismisses that layer, the second reaches the dialog itself.
    fireEvent.keyDown(dialog, { key: 'Escape' })
    fireEvent.keyDown(dialog, { key: 'Escape' })
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('shows a singular "1 file" count for exactly one file', async () => {
    vi.mocked(listMissionFiles).mockResolvedValue({ files: [files[0]], truncated: false })
    render(<ArtifactsSection missionId="m1" phase="execute" workspace="ws-1" />)
    expect(await screen.findByText('1 file')).toBeTruthy()
  })

  it('shows the destructive error line when the file listing fails', async () => {
    vi.mocked(listMissionFiles).mockRejectedValue(new Error('workspace gone'))
    render(<ArtifactsSection missionId="m1" phase="execute" workspace="ws-1" refs={refs} />)
    expect(await screen.findByText('workspace gone')).toBeTruthy()
  })

  it('opens the Promote to KB dialog from the workspace panel action', async () => {
    vi.mocked(listMissionFiles).mockResolvedValue({
      files: [{ path: 'README.md', size: 10, mtime: '2026-01-01T00:00:00Z', declared: false }],
      truncated: false,
    })
    render(
      <ArtifactsSection
        missionId="m1"
        phase="done"
        workspace="ws-1"
        refs={[{ id: 'a1', mime: 'text/markdown', name: 'README.md' }]}
      />,
    )
    const btn = await screen.findByRole('button', {
      name: 'Promote workspace markdown artifacts to the knowledge base',
    })
    fireEvent.click(btn)
    expect(await screen.findByText('Promote to knowledge base')).toBeInTheDocument()
  })

  it('renders refs chips alone when the workspace is gone', () => {
    render(<ArtifactsSection missionId="m1" phase="terminal" workspace={undefined} refs={refs} />)
    expect(screen.getByText('Files')).toBeInTheDocument()
    expect(screen.getByText('report.md')).toBeInTheDocument()
    expect(listMissionFiles).not.toHaveBeenCalled()
  })

  it('does not duplicate refs chips inside the workspace panel', async () => {
    vi.mocked(listMissionFiles).mockResolvedValue({ files, truncated: false })
    render(<ArtifactsSection missionId="m1" phase="execute" workspace="ws-1" refs={refs} />)
    await screen.findByText('big.bin')
    expect(screen.getAllByText('Files')).toHaveLength(1)
    expect(screen.queryByText('report.md')).toBeNull()
  })

  it('renders the panel (not chips) when the workspace has no files but has refs', async () => {
    render(<ArtifactsSection missionId="m1" phase="execute" workspace="ws-1" refs={refs} />)
    await waitFor(() => expect(vi.mocked(listMissionFiles)).toHaveBeenCalled())
    expect(screen.getByText('Files')).toBeInTheDocument()
    expect(screen.getByText('No files yet.')).toBeInTheDocument()
    expect(screen.queryByText('report.md')).toBeNull()
  })

  it('renders nothing when there is no workspace and no refs', () => {
    const { container } = render(
      <ArtifactsSection missionId="m1" phase="execute" workspace={undefined} refs={[]} />,
    )
    expect(container.firstChild).toBeNull()
  })

  const filesWithMarkdown: MissionFile[] = [
    ...files,
    { path: 'README.md', size: 100, mtime: '2026-01-01T00:00:00Z', declared: false },
  ]

  it('hides Export all as PDF when pdf_export_enabled is false', async () => {
    vi.mocked(listMissionFiles).mockResolvedValue({ files: filesWithMarkdown, truncated: false })
    render(<ArtifactsSection missionId="m2" phase="execute" workspace="ws-2" />)
    await screen.findByText('README.md')
    expect(
      screen.queryByRole('button', { name: 'Export all workspace markdown as one merged PDF' }),
    ).toBeNull()
  })

  it('hides Export all as PDF when no markdown files exist, even if enabled', async () => {
    vi.mocked(getSettings).mockResolvedValue({ settings: { pdf_export_enabled: true }, values: {} })
    vi.mocked(listMissionFiles).mockResolvedValue({ files, truncated: false })
    render(<ArtifactsSection missionId="m3" phase="execute" workspace="ws-3" />)
    await screen.findByText('big.bin')
    expect(
      screen.queryByRole('button', { name: 'Export all workspace markdown as one merged PDF' }),
    ).toBeNull()
  })

  it('shows Export all as PDF when enabled and markdown exists, downloading on click', async () => {
    vi.mocked(getSettings).mockResolvedValue({ settings: { pdf_export_enabled: true }, values: {} })
    vi.mocked(listMissionFiles).mockResolvedValue({ files: filesWithMarkdown, truncated: false })
    vi.mocked(exportMissionPdf).mockResolvedValue({ attachment_id: 'att-9', cached: true })
    vi.mocked(downloadMissionPdfExport).mockResolvedValue(undefined)
    render(<ArtifactsSection missionId="m4" missionName="My Mission" phase="execute" workspace="ws-4" />)

    const btn = await screen.findByRole('button', { name: 'Export all workspace markdown as one merged PDF' })
    fireEvent.click(btn)

    expect(exportMissionPdf).toHaveBeenCalledWith('m4')
    await screen.findByRole('button', { name: 'Export all workspace markdown as one merged PDF' })
    expect(downloadMissionPdfExport).toHaveBeenCalledWith('att-9', 'My Mission.pdf')
  })

  describe('promote to knowledge base', () => {
    it('hides the action when the mission is not done', () => {
      render(<ArtifactsSection missionId="m1" phase="execute" workspace={undefined} refs={refs} />)
      expect(screen.queryByText('Promote to KB')).toBeNull()
    })

    it('hides the action when there are no markdown artifacts', () => {
      render(
        <ArtifactsSection
          missionId="m1"
          phase="done"
          workspace={undefined}
          refs={[{ id: 'a1', mime: 'application/pdf', name: 'chart.pdf' }]}
        />,
      )
      expect(screen.queryByText('Promote to KB')).toBeNull()
    })

    it('opens a dialog with a collection picker and promotes on submit', async () => {
      vi.mocked(listKbCollections).mockResolvedValue([
        { id: 'c1', name: 'Reports', description: '', doc_count: 0, chunk_count: 0, failed_count: 0, retrieval_weight: 1.0, created_at: '', updated_at: '' },
      ])
      vi.mocked(promoteMissionToKB).mockResolvedValue({ promoted: 1 })
      render(<ArtifactsSection missionId="m1" phase="done" workspace={undefined} refs={refs} />)

      fireEvent.click(screen.getByText('Promote to KB'))
      expect(await screen.findByText('Promote to knowledge base')).toBeInTheDocument()

      fireEvent.click(screen.getByRole('combobox'))
      fireEvent.click(await screen.findByText('Reports'))
      fireEvent.click(screen.getByRole('button', { name: 'Promote' }))

      await waitFor(() => expect(promoteMissionToKB).toHaveBeenCalledWith('m1', 'c1'))
      expect(await screen.findByText(/Promoted 1 document/)).toBeInTheDocument()
    })

    it('disables Promote until a collection is picked', async () => {
      vi.mocked(listKbCollections).mockResolvedValue([
        { id: 'c1', name: 'Reports', description: '', doc_count: 0, chunk_count: 0, failed_count: 0, retrieval_weight: 1.0, created_at: '', updated_at: '' },
      ])
      render(<ArtifactsSection missionId="m1" phase="done" workspace={undefined} refs={refs} />)

      fireEvent.click(screen.getByText('Promote to KB'))
      await screen.findByText('Promote to knowledge base')
      expect(screen.getByRole('button', { name: 'Promote' })).toBeDisabled()
    })
  })
})
