import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { NotesDialog } from './NotesDialog'
import { maxNoteBytes, noteBytes } from './notes'
import { makeNote } from './testFixtures'

describe('NotesDialog', () => {
  it('counts bytes, not characters', () => {
    render(<NotesDialog open onOpenChange={vi.fn()} onSave={vi.fn()} />)
    fireEvent.change(screen.getByLabelText('Content'), { target: { value: 'héllo' } })
    expect(screen.getByText(`6 / ${maxNoteBytes} bytes`)).toBeInTheDocument()
    expect(noteBytes('€')).toBe(3)
  })

  it('disables Save once the content is over the cap', () => {
    render(<NotesDialog open onOpenChange={vi.fn()} onSave={vi.fn()} />)
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'big' } })
    fireEvent.change(screen.getByLabelText('Content'), { target: { value: 'x'.repeat(maxNoteBytes + 1) } })
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
    expect(screen.getByRole('alert')).toHaveTextContent(`at most ${maxNoteBytes} bytes`)
  })

  it('saves a new note and closes', async () => {
    const onSave = vi.fn().mockResolvedValue(true)
    const onOpenChange = vi.fn()
    render(<NotesDialog open onOpenChange={onOpenChange} onSave={onSave} />)
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'progress' } })
    fireEvent.change(screen.getByLabelText('Content'), { target: { value: 'seen #1' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(onSave).toHaveBeenCalledWith('progress', 'seen #1'))
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false))
  })

  it('stays open when the save fails', async () => {
    const onSave = vi.fn().mockResolvedValue(false)
    const onOpenChange = vi.fn()
    render(<NotesDialog open onOpenChange={onOpenChange} onSave={onSave} />)
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'progress' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(onSave).toHaveBeenCalled())
    expect(onOpenChange).not.toHaveBeenCalled()
  })

  it('rejects a bad note name', () => {
    const onSave = vi.fn()
    render(<NotesDialog open onOpenChange={vi.fn()} onSave={onSave} />)
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'Bad Name' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(screen.getByRole('alert')).toHaveTextContent('lowercase')
    expect(onSave).not.toHaveBeenCalled()
  })

  it('locks the name when editing and seeds the content', async () => {
    const onSave = vi.fn().mockResolvedValue(true)
    render(<NotesDialog open onOpenChange={vi.fn()} note={makeNote()} onSave={onSave} />)
    expect(screen.getByLabelText('Name')).toBeDisabled()
    expect(screen.getByLabelText('Name')).toHaveValue('progress')
    expect(screen.getByLabelText('Content')).toHaveValue('Last seen PR: #42')
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(onSave).toHaveBeenCalledWith('progress', 'Last seen PR: #42'))
  })
})
