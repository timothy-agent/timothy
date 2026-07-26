import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { MissionFile } from '../../api/types'
import { FileViewer } from './FileViewer'

vi.mock('../../api/client', () => ({
  downloadMissionFile: vi.fn(),
  fetchMissionFileBlob: vi.fn(),
  missionFilePreviewCap: 1_000_000,
  MissionFileTooLargeError: class MissionFileTooLargeError extends Error {},
}))

import { MissionFileTooLargeError, fetchMissionFileBlob } from '../../api/client'

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
  URL.createObjectURL = vi.fn(() => 'blob:mock')
  URL.revokeObjectURL = vi.fn()
})

function file(path: string, size = 5): MissionFile {
  return { path, size, mtime: '2026-01-01T00:00:00Z', declared: false }
}

describe('FileViewer', () => {
  it('renders code files with syntax highlighting', async () => {
    vi.mocked(fetchMissionFileBlob).mockResolvedValue(new Blob(['package main']))
    const { container } = render(<FileViewer missionId="m1" file={file('main.go')} />)

    await screen.findByText('package', { exact: false })
    const code = container.querySelector('code.hljs')
    expect(code?.textContent).toBe('package main')
    expect(code?.querySelector('.hljs-keyword')).toBeTruthy()
  })

  it('renders markdown rendered by default, with a toggle to raw', async () => {
    vi.mocked(fetchMissionFileBlob).mockResolvedValue(new Blob(['# Hello world']))
    const { container } = render(<FileViewer missionId="m1" file={file('README.md')} />)

    expect(await screen.findByRole('heading', { name: 'Hello world' })).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: 'Raw' }))
    await screen.findByText('Hello', { exact: false })
    const code = container.querySelector('code.hljs')
    expect(code?.textContent).toBe('# Hello world')
  })

  it('renders images via an object URL', async () => {
    vi.mocked(fetchMissionFileBlob).mockResolvedValue(new Blob(['bytes']))
    render(<FileViewer missionId="m1" file={file('logo.png')} />)

    const img = await screen.findByRole('img')
    expect(img.getAttribute('src')).toBe('blob:mock')
  })

  it('shows a too-large message without fetching preview content past the cap', async () => {
    vi.mocked(fetchMissionFileBlob).mockRejectedValue(new MissionFileTooLargeError('too big'))
    render(<FileViewer missionId="m1" file={file('main.go')} />)

    expect(await screen.findByText(/too large to preview/)).toBeTruthy()
  })

  it('shows an unsupported message for unknown file types without calling fetchMissionFileBlob', async () => {
    render(<FileViewer missionId="m1" file={file('archive.zip')} />)

    expect(await screen.findByText(/Can.t preview this file type/)).toBeTruthy()
    expect(fetchMissionFileBlob).not.toHaveBeenCalled()
  })
})
