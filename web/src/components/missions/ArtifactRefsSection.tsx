import { File, FileText, Music, Video } from 'lucide-react'
import { useState } from 'react'
import type { MediaRef } from '../../api/types'
import { AttachmentViewer, mimeLabel } from '../AttachmentViewer'
import { Badge } from '../ui/badge'

// artifactChipIcon picks a chip's icon by mime, same mapping as
// Message.tsx's documentChipIcon.
function artifactChipIcon(mime: string) {
  if (mime.startsWith('video/')) return Video
  if (mime.startsWith('audio/')) return Music
  if (mime === 'text/plain' || mime === 'text/markdown') return FileText
  return File
}

// ArtifactRefChips renders a terminal mission's artifact-store refs
// (mission.artifact_refs) as clickable chips through AttachmentViewer —
// durable copies that keep working after mission/workspace cleanup,
// integrated into ArtifactsSection's panel (or rendered alone when the
// workspace is gone).
export function ArtifactRefChips({ refs }: { refs: MediaRef[] }) {
  const [viewerAttachment, setViewerAttachment] = useState<MediaRef | null>(null)
  if (refs.length === 0) return null
  return (
    <>
      <div className="flex flex-wrap gap-1.5">
        {refs.map((ref) => {
          const Icon = artifactChipIcon(ref.mime)
          return (
            <Badge key={ref.id} variant="outline" size="sm" asChild>
              <button type="button" title={ref.name ?? ref.id.slice(0, 8)} onClick={() => setViewerAttachment(ref)}>
                <Icon className="size-3.5" aria-hidden />
                {ref.name ?? mimeLabel(ref.mime)}
              </button>
            </Badge>
          )
        })}
      </div>
      <AttachmentViewer
        open={viewerAttachment !== null}
        onOpenChange={(open) => {
          if (!open) setViewerAttachment(null)
        }}
        attachment={viewerAttachment}
      />
    </>
  )
}
