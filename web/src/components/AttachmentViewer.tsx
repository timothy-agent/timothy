import {
  Cancel01Icon,
  Copy01Icon,
  Download04Icon,
  ImageNotFound01Icon,
  Loading03Icon,
  Tick02Icon,
} from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useEffect, useState } from 'react'
import { fetchAttachmentBlob } from '../api/client'
import { attachmentURLCache } from '../lib/attachmentCache'
import { FileCodeBlock, FileMarkdownBlock } from './FilePreviewBlocks'
import { Button } from './ui/button'
import { Dialog, DialogClose, DialogContent, DialogTitle } from './ui/dialog'

// AttachmentViewer is a fullscreen modal for one chat attachment,
// dispatching on mime: images get a lightbox, video/audio get native
// controls, PDFs render in an iframe, and text/markdown reuse
// FileViewer's reader halves (FilePreviewBlocks.tsx). Reuses
// Message.tsx's attachmentURLCache so an already-rendered thumbnail's
// blob URL is never refetched.
export function AttachmentViewer({
  open,
  onOpenChange,
  attachment,
  localUrl,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  attachment: { id: string; mime: string; name?: string } | null
  // An optimistic item's own object URL from the composer, used
  // directly instead of fetching.
  localUrl?: string
}) {
  const [url, setUrl] = useState<string | undefined>(undefined)
  const [text, setText] = useState<string | undefined>(undefined)
  const [failed, setFailed] = useState(false)
  const [copied, setCopied] = useState(false)
  const [showRaw, setShowRaw] = useState(false)

  const id = attachment?.id
  const mime = attachment?.mime ?? ''
  const isText = mime === 'text/plain' || mime === 'text/markdown'

  useEffect(() => {
    setFailed(false)
    setText(undefined)
    setShowRaw(false)
    if (!open || !id) {
      setUrl(undefined)
      return
    }
    const cached = localUrl ?? attachmentURLCache.get(id)
    if (cached) {
      setUrl(cached)
      if (isText) {
        fetch(cached)
          .then((r) => r.text())
          .then(setText)
          .catch(() => setFailed(true))
      }
      return
    }
    let stale = false
    fetchAttachmentBlob(id)
      .then(async (blob) => {
        if (stale) return
        const objectUrl = URL.createObjectURL(blob)
        attachmentURLCache.set(id, objectUrl)
        setUrl(objectUrl)
        if (isText) setText(await blob.text())
      })
      .catch(() => {
        if (!stale) setFailed(true)
      })
    return () => {
      stale = true
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- isText derives from mime, which is stable per attachment id
  }, [open, id, localUrl])

  if (!attachment) return null

  const label = mimeLabel(mime)
  const displayName = attachment.name ?? `${label} (${attachment.id.slice(0, 8)})`

  const copyText = async () => {
    if (text === undefined) return
    try {
      await navigator.clipboard.writeText(text)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    } catch {
      // Clipboard unavailable: no-op.
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        showCloseButton={false}
        className="flex h-[calc(100vh-4rem)] max-w-[calc(100vw-4rem)] flex-col p-0 sm:max-w-[calc(100vw-4rem)]"
        aria-describedby={undefined}
      >
        <div className="flex items-center justify-between gap-3 border-b border-border bg-muted/50 px-4 py-2.5">
          <div className="min-w-0 flex-1">
            <DialogTitle className="truncate text-sm font-medium">{displayName}</DialogTitle>
            <p className="text-xs text-muted-foreground">{mime}</p>
          </div>
          <div className="flex items-center gap-1.5">
            {isText && text !== undefined && (
              <>
                {mime === 'text/markdown' && (
                  <Button variant="outline" size="sm" onClick={() => setShowRaw((v) => !v)}>
                    {showRaw ? 'Rendered' : 'Source'}
                  </Button>
                )}
                <Button variant="ghost" size="sm" onClick={() => void copyText()}>
                  <HugeiconsIcon icon={copied ? Tick02Icon : Copy01Icon} />
                  {copied ? 'Copied' : 'Copy'}
                </Button>
              </>
            )}
            {url && (
              <Button variant="ghost" size="sm" asChild>
                <a href={url} download={attachment.name || attachment.id}>
                  <HugeiconsIcon icon={Download04Icon} />
                  Download
                </a>
              </Button>
            )}
            {/* DialogContent's built-in close X is suppressed
                (showCloseButton={false}) since it's absolutely
                positioned top-right and collided with Download above —
                this sits in-flow in the header row instead, same fix
                FullscreenDialog uses for its own header controls. */}
            <DialogClose asChild>
              <Button variant="ghost" size="icon-sm" aria-label="Close">
                <HugeiconsIcon icon={Cancel01Icon} />
              </Button>
            </DialogClose>
          </div>
        </div>
        <div className="flex min-h-0 flex-1 items-center justify-center overflow-auto bg-zinc-950/2 dark:bg-black/40">
          {failed && (
            <div className="flex flex-col items-center gap-2 text-muted-foreground">
              <HugeiconsIcon icon={ImageNotFound01Icon} className="size-8" />
              <p className="text-sm">Could not load this attachment.</p>
            </div>
          )}
          {!failed && !url && (
            <HugeiconsIcon icon={Loading03Icon} className="size-6 animate-spin text-muted-foreground" />
          )}
          {!failed && url && mime.startsWith('image/') && (
            <img src={url} alt={displayName} className="max-h-full max-w-full object-contain" />
          )}
          {!failed && url && mime.startsWith('video/') && (
            // eslint-disable-next-line jsx-a11y/media-has-caption -- attachment source carries no caption track
            <video src={url} controls className="max-h-full max-w-full" />
          )}
          {!failed && url && mime.startsWith('audio/') && (
            // eslint-disable-next-line jsx-a11y/media-has-caption -- audio has no caption track
            <audio src={url} controls className="w-full max-w-md" />
          )}
          {!failed && url && mime === 'application/pdf' && (
            <iframe src={url} title={displayName} className="size-full border-0" />
          )}
          {!failed && isText && text !== undefined && (
            <div className="size-full overflow-auto">
              {mime === 'text/markdown' ? (
                <FileMarkdownBlock text={text} raw={showRaw} />
              ) : (
                <FileCodeBlock code={text} path={attachment.name ?? 'file.txt'} />
              )}
            </div>
          )}
        </div>
      </DialogContent>
    </Dialog>
  )
}

// mimeLabel is the short type label used in headers/chips when no
// filename is present.
export function mimeLabel(mime: string): string {
  switch (mime) {
    case 'application/pdf':
      return 'PDF'
    case 'text/markdown':
      return 'MD'
    case 'text/plain':
      return 'TXT'
    case 'video/mp4':
      return 'MP4'
    case 'video/webm':
      return 'WEBM'
    case 'audio/mpeg':
      return 'MP3'
    case 'audio/wav':
      return 'WAV'
    case 'audio/ogg':
      return 'OGG'
    default:
      return mime.split('/')[1]?.toUpperCase() ?? 'FILE'
  }
}
