import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { MissionFile } from '../../api/types'
import { buildFileTree } from './fileTree'
import { FileTreeView } from './FileTreeView'

afterEach(cleanup)

function file(path: string, declared = false): MissionFile {
  return { path, size: 5, mtime: '2026-01-01T00:00:00Z', declared }
}

describe('FileTreeView', () => {
  it('renders a folder open by default and collapses it on click', () => {
    const nodes = buildFileTree([file('src/main.go')])
    render(<FileTreeView nodes={nodes} onSelect={() => {}} />)

    const folderButton = screen.getByRole('button', { name: /src/ })
    expect(folderButton).toHaveAttribute('aria-expanded', 'true')
    expect(folderButton.querySelector('svg.lucide-chevron-down')).toBeTruthy()
    expect(folderButton.querySelector('svg.lucide-folder-open')).toBeTruthy()
    expect(screen.getByText('main.go')).toBeInTheDocument()

    fireEvent.click(folderButton)
    expect(folderButton).toHaveAttribute('aria-expanded', 'false')
    expect(folderButton.querySelector('svg.lucide-chevron-right')).toBeTruthy()
    expect(folderButton.querySelector('svg.lucide-folder:not(.lucide-folder-open)')).toBeTruthy()
    expect(screen.queryByText('main.go')).not.toBeInTheDocument()
  })

  it('calls onSelect with the file node when a file row is clicked', () => {
    const onSelect = vi.fn()
    const nodes = buildFileTree([file('README.md')])
    render(<FileTreeView nodes={nodes} onSelect={onSelect} />)

    fireEvent.click(screen.getByText('README.md'))
    expect(onSelect).toHaveBeenCalledWith(expect.objectContaining({ path: 'README.md' }))
  })

  it('marks the selected file with aria-current', () => {
    const nodes = buildFileTree([file('a.txt'), file('b.txt')])
    render(<FileTreeView nodes={nodes} selectedPath="b.txt" onSelect={() => {}} />)

    expect(screen.getByText('a.txt').closest('button')).not.toHaveAttribute('aria-current')
    expect(screen.getByText('b.txt').closest('button')).toHaveAttribute('aria-current', 'true')
  })

  it('renders a distinct icon for an image file', () => {
    const nodes = buildFileTree([file('photo.png')])
    render(<FileTreeView nodes={nodes} onSelect={() => {}} />)
    const row = screen.getByText('photo.png').closest('button')
    expect(row?.querySelector('svg')).toHaveClass('lucide-image')
  })

  it('renders a code-file icon for a code file', () => {
    const nodes = buildFileTree([file('main.go')])
    render(<FileTreeView nodes={nodes} onSelect={() => {}} />)
    const row = screen.getByText('main.go').closest('button')
    expect(row?.querySelector('svg')).toHaveClass('lucide-file-code')
  })

  it('renders the generic file icon for an unsupported file', () => {
    const nodes = buildFileTree([file('data.bin')])
    render(<FileTreeView nodes={nodes} onSelect={() => {}} />)
    const row = screen.getByText('data.bin').closest('button')
    expect(row?.querySelector('svg')).toHaveClass('lucide-file')
    expect(row?.querySelector('svg')).not.toHaveClass('lucide-file-code')
  })

  it('shows a declared badge only for declared files', () => {
    const nodes = buildFileTree([file('declared.txt', true), file('plain.txt', false)])
    render(<FileTreeView nodes={nodes} onSelect={() => {}} />)

    expect(screen.getAllByText('declared')).toHaveLength(1)
    expect(screen.getByText('plain.txt').closest('li')?.textContent).not.toContain('declared')
  })
})
