import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { MissionFile } from '../../api/types'
import { ArtifactsSection } from './ArtifactsSection'

vi.mock('../../api/client', () => ({
  listMissionFiles: vi.fn(),
  downloadMissionFile: vi.fn(),
  downloadMissionArchive: vi.fn(),
  fetchMissionFileBlob: vi.fn(),
  missionFilePreviewCap: 1_000_000,
  MissionFileTooLargeError: class MissionFileTooLargeError extends Error {},
}))

import { fetchMissionFileBlob, listMissionFiles } from '../../api/client'

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(listMissionFiles).mockResolvedValue({ files: [], truncated: false })
  vi.mocked(fetchMissionFileBlob).mockResolvedValue(new Blob(['hello']))
})

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
    expect(screen.getByRole('button', { name: /Download all/ })).not.toBeDisabled()
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
})
