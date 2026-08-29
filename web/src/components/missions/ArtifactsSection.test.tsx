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
  missionFilePreviewCap: 1_000_000,
  MissionFileTooLargeError: class MissionFileTooLargeError extends Error {},
}))

import {
  downloadMissionPdfExport,
  exportMissionPdf,
  fetchAttachmentBlob,
  fetchMissionFileBlob,
  getSettings,
  listMissionFiles,
} from '../../api/client'

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(listMissionFiles).mockResolvedValue({ files: [], truncated: false })
  vi.mocked(fetchMissionFileBlob).mockResolvedValue(new Blob(['hello']))
  vi.mocked(fetchAttachmentBlob).mockResolvedValue(new Blob(['hello']))
  vi.mocked(getSettings).mockResolvedValue({ settings: {}, values: {} })
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
    expect(screen.getByText('Artifacts')).toBeTruthy()
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

  it('renders refs chips alone when the workspace is gone', () => {
    render(<ArtifactsSection missionId="m1" phase="terminal" workspace={undefined} refs={refs} />)
    expect(screen.getByText('Artifacts')).toBeInTheDocument()
    expect(screen.getByText('report.md')).toBeInTheDocument()
    expect(listMissionFiles).not.toHaveBeenCalled()
  })

  it('does not duplicate refs chips inside the workspace panel', async () => {
    vi.mocked(listMissionFiles).mockResolvedValue({ files, truncated: false })
    render(<ArtifactsSection missionId="m1" phase="execute" workspace="ws-1" refs={refs} />)
    await screen.findByText('big.bin')
    expect(screen.getAllByText('Artifacts')).toHaveLength(1)
    expect(screen.queryByText('report.md')).toBeNull()
  })

  it('renders the panel (not chips) when the workspace has no files but has refs', async () => {
    render(<ArtifactsSection missionId="m1" phase="execute" workspace="ws-1" refs={refs} />)
    await waitFor(() => expect(vi.mocked(listMissionFiles)).toHaveBeenCalled())
    expect(screen.getByText('Artifacts')).toBeInTheDocument()
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
})
