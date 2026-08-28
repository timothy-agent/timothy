import { File01Icon, FileMusicIcon, FileVideoIcon, Pdf02Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useState } from 'react'
import type { MediaRef } from '../../api/types'
import { AttachmentViewer, mimeLabel } from '../AttachmentViewer'

// artifactChipIcon picks a chip's icon by mime, same mapping as
// Message.tsx's documentChipIcon.
function artifactChipIcon(mime: string) {
  if (mime.startsWith('video/')) return FileVideoIcon
  if (mime.startsWith('audio/')) return FileMusicIcon
  if (mime === 'text/plain' || mime === 'text/markdown') return File01Icon
  return Pdf02Icon
}

// ArtifactRefsSection renders a terminal mission's artifact-store refs
// (mission.artifact_refs) as clickable chips through AttachmentViewer —
// unlike ArtifactsSection (which browses the live workspace and
// disappears once it's deleted), these refs are durable copies that
// keep working after mission/workspace cleanup.
export function ArtifactRefsSection({ refs }: { refs: MediaRef[] }) {
  const [viewerAttachment, setViewerAttachment] = useState<MediaRef | null>(null)
  if (refs.length === 0) return null
  return (
    <section>
      <h2 className="mb-2 text-sm font-semibold tracking-tight">Artifacts</h2>
      <div className="flex flex-wrap gap-1.5">
        {refs.map((ref) => (
          <button
            key={ref.id}
            type="button"
            title={ref.name ?? ref.id.slice(0, 8)}
            onClick={() => setViewerAttachment(ref)}
            className="flex items-center gap-1 rounded-lg border border-zinc-950/10 bg-zinc-100 px-2 py-1 text-xs text-zinc-500 transition hover:bg-zinc-200 dark:border-white/10 dark:bg-zinc-800 dark:text-zinc-400 dark:hover:bg-zinc-700"
          >
            <HugeiconsIcon icon={artifactChipIcon(ref.mime)} className="size-3.5" />
            {ref.name ?? mimeLabel(ref.mime)}
          </button>
        ))}
      </div>
      <AttachmentViewer
        open={viewerAttachment !== null}
        onOpenChange={(open) => {
          if (!open) setViewerAttachment(null)
        }}
        attachment={viewerAttachment}
      />
    </section>
  )
}
