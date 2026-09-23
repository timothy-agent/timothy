import { useState } from 'react'

import type { AutomationNote } from '../../api/types'
import { Field } from '../timothy/field'
import { Spinner } from '../timothy/spinner'
import { Button } from '../ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '../ui/dialog'
import { Input } from '../ui/input'
import { Textarea } from '../ui/textarea'
import { maxNoteBytes, noteBytes, noteNameRe } from './notes'

// NotesDialog adds a note, or edits one (note set) with its name locked.
// onSave resolves true when the note was stored.
export function NotesDialog({
  open,
  onOpenChange,
  note,
  onSave,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  note?: AutomationNote | null
  onSave: (name: string, content: string) => Promise<boolean>
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        {open && <NoteForm key={note?.name ?? ''} note={note} onSave={onSave} onClose={() => onOpenChange(false)} />}
      </DialogContent>
    </Dialog>
  )
}

function NoteForm({
  note,
  onSave,
  onClose,
}: {
  note?: AutomationNote | null
  onSave: (name: string, content: string) => Promise<boolean>
  onClose: () => void
}) {
  const [name, setName] = useState(note?.name ?? '')
  const [content, setContent] = useState(note?.content ?? '')
  const [touched, setTouched] = useState(false)
  const [busy, setBusy] = useState(false)
  const bytes = noteBytes(content)
  const over = bytes > maxNoteBytes
  const nameError = !note && touched && !noteNameRe.test(name) ? 'Use 1 to 64 lowercase letters, digits, - or _.' : undefined

  const save = async () => {
    setTouched(true)
    if (!noteNameRe.test(name) || over) return
    setBusy(true)
    const ok = await onSave(name, content)
    setBusy(false)
    if (ok) onClose()
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle>{note ? `Edit ${note.name}` : 'Add note'}</DialogTitle>
        <DialogDescription>Each run reads its notes and can update them.</DialogDescription>
      </DialogHeader>
      <div className="space-y-5">
        <Field label="Name" error={nameError}>
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            onBlur={() => setTouched(true)}
            disabled={!!note}
            placeholder="progress"
            className="font-mono"
          />
        </Field>
        <Field label="Content" error={over ? `Notes hold at most ${maxNoteBytes} bytes.` : undefined}>
          <Textarea value={content} onChange={(e) => setContent(e.target.value)} rows={8} className="font-mono text-xs" />
        </Field>
        <p className={over ? 'text-xs text-destructive tabular-nums' : 'text-xs text-muted-foreground tabular-nums'}>
          {bytes} / {maxNoteBytes} bytes
        </p>
      </div>
      <DialogFooter>
        <Button variant="outline" onClick={onClose} disabled={busy}>
          Cancel
        </Button>
        <Button onClick={() => void save()} disabled={over || busy} aria-busy={busy}>
          {busy && <Spinner size="sm" label="Saving" className="text-current" />}
          Save
        </Button>
      </DialogFooter>
    </>
  )
}
