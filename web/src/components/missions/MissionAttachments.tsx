import { Attachment02Icon, Cancel01Icon, Loading03Icon, Pdf02Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useRef } from 'react'
import { toast } from 'sonner'
import { uploadAttachment } from '../../api/client'
import {
  isDocumentFile,
  maxAttachmentBytes,
  maxAttachments,
  type PendingAttachment,
} from '../Composer'
import { Button } from '../ui/button'

// MissionAttachments is the mission-create form's document attachment
// picker (PDF, Markdown, text): an "Attach file" button plus a chip
// strip (name, uploading spinner, remove button) — a simplified
// variant of Composer.tsx's uploadFiles/removeAttachment flow with no
// paste/drag support, since a mission's create form isn't a message
// box.
export function MissionAttachments({
  attachments,
  onChange,
}: {
  attachments: PendingAttachment[]
  onChange: (attachments: PendingAttachment[]) => void
}) {
  const inputRef = useRef<HTMLInputElement>(null)

  async function uploadFiles(files: File[]) {
    for (const file of files) {
      if (!isDocumentFile(file)) {
        toast.error(`${file.name || 'file'}: only PDF, Markdown, and text attachments are supported`)
        continue
      }
      if (file.size > maxAttachmentBytes) {
        toast.error(`${file.name || 'file'}: exceeds the 10MB limit`)
        continue
      }
      if (attachments.length >= maxAttachments) {
        toast.error(`You can attach up to ${maxAttachments} files`)
        break
      }
      const tempId = crypto.randomUUID()
      const placeholder: PendingAttachment = {
        id: tempId,
        mime: file.type,
        previewUrl: '',
        name: file.name,
        uploading: true,
      }
      attachments = [...attachments, placeholder]
      onChange(attachments)
      try {
        const uploaded = await uploadAttachment(file)
        attachments = attachments.map((a) =>
          a.id === tempId ? { ...a, id: uploaded.id, uploading: false } : a,
        )
        onChange(attachments)
      } catch (err) {
        attachments = attachments.filter((a) => a.id !== tempId)
        onChange(attachments)
        toast.error(err instanceof Error ? err.message : 'Upload failed')
      }
    }
  }

  function removeAttachment(id: string) {
    onChange(attachments.filter((a) => a.id !== id))
  }

  return (
    <div className="space-y-2">
      <input
        ref={inputRef}
        type="file"
        accept="application/pdf,.md,.txt,text/plain,text/markdown"
        multiple
        className="hidden"
        onChange={(e) => {
          const files = [...(e.target.files ?? [])]
          e.target.value = ''
          if (files.length > 0) void uploadFiles(files)
        }}
      />
      <Button type="button" variant="outline" size="sm" onClick={() => inputRef.current?.click()}>
        <HugeiconsIcon icon={Attachment02Icon} className="size-4" />
        Attach file
      </Button>
      {attachments.length > 0 && (
        <div className="flex flex-wrap gap-2">
          {attachments.map((a) => (
            <div
              key={a.id}
              className="group relative flex items-center gap-1.5 rounded-lg border border-border bg-muted/30 py-1 pr-1.5 pl-2 text-xs"
            >
              <HugeiconsIcon icon={Pdf02Icon} className="size-3.5 text-muted-foreground" />
              <span className="max-w-40 truncate">{a.name ?? 'Document'}</span>
              {a.uploading ? (
                <HugeiconsIcon icon={Loading03Icon} className="size-3 animate-spin text-muted-foreground" />
              ) : (
                <button
                  type="button"
                  onClick={() => removeAttachment(a.id)}
                  aria-label={`Remove ${a.name ?? 'attachment'}`}
                  className="flex size-3.5 items-center justify-center rounded-full text-muted-foreground hover:text-foreground"
                >
                  <HugeiconsIcon icon={Cancel01Icon} className="size-3" />
                </button>
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
