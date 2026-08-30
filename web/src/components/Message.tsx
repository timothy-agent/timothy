import {
  Copy01Icon,
  File01Icon,
  FileMusicIcon,
  FileVideoIcon,
  ImageNotFound01Icon,
  Link04Icon,
  Loading03Icon,
  Pdf02Icon,
  ReloadIcon,
  Tick02Icon,
} from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useEffect, useRef, useState } from 'react'
import ReactMarkdown from 'react-markdown'
import { fetchAttachmentBlob } from '../api/client'
import type { ImageRef, MediaRef } from '../api/types'
import { ActivityLine } from './Activity'
import { AttachmentViewer, mimeLabel } from './AttachmentViewer'
import { CodeBlock } from './CodeBlock'
import { ModelBadge } from './ModelBadge'
import { Badge } from './ui/badge'
import { collapseRepeatedTail, splitSources } from '../lib/citations'
import { attachmentURLCache } from '../lib/attachmentCache'
import type { AssistantState } from '../lib/chat'
import { compact, formatDuration, money } from '../lib/format'
import { rehypePlugins, remarkPlugins } from '../lib/markdown'
import { cn } from '../lib/utils'

// AuthedImage renders one attachment thumbnail. GET
// /v1/attachments/{id} requires the bearer header, so a bare <img
// src> can't fetch it directly — this fetches the bytes through the
// authed client and renders them as a blob object URL, with a loading
// shimmer while in flight and a broken-image fallback on failure.
// Click opens the AttachmentViewer modal on the same resolved blob URL
// — a raw /v1/attachments/{id} href would 401 without the bearer
// header, and a new tab loses the app's own reader/download chrome.
// `localUrl`, when given (an optimistic item's own object URL from
// the composer), is used directly and never fetched.
function AuthedImage({
  id,
  mime,
  localUrl,
  onOpen,
}: {
  id: string
  mime: string
  localUrl?: string
  onOpen: () => void
}) {
  const [url, setUrl] = useState<string | undefined>(localUrl ?? attachmentURLCache.get(id))
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    if (localUrl || url) return
    let stale = false
    fetchAttachmentBlob(id)
      .then((blob) => {
        if (stale) return
        const objectUrl = URL.createObjectURL(blob)
        attachmentURLCache.set(id, objectUrl)
        setUrl(objectUrl)
      })
      .catch(() => {
        if (!stale) setFailed(true)
      })
    return () => {
      stale = true
    }
  }, [id, localUrl, url])

  if (failed) {
    return (
      <div
        data-testid="attachment-error"
        className="flex size-full min-h-24 items-center justify-center rounded-lg bg-zinc-100 text-zinc-400 dark:bg-zinc-800 dark:text-zinc-500"
        title={id}
      >
        <HugeiconsIcon icon={ImageNotFound01Icon} className="size-6" />
      </div>
    )
  }

  if (!url) {
    return (
      <div
        data-testid="attachment-loading"
        className="flex size-full min-h-24 items-center justify-center rounded-lg bg-zinc-100 dark:bg-zinc-800"
      >
        <HugeiconsIcon icon={Loading03Icon} className="size-5 animate-spin text-zinc-400" />
      </div>
    )
  }

  return (
    <img
      src={url}
      alt={mime}
      onClick={onOpen}
      className="max-h-50 max-w-full cursor-pointer rounded-lg object-cover"
    />
  )
}

// ImageGrid renders a set of image attachments (a user message's
// attached images, or an assistant turn's generated media) above the
// text. Optimistic items carry their own local object URL (localUrls
// keyed by id) so the just-sent thumbnail never round-trips the
// network.
function ImageGrid({
  images,
  localUrls,
  onOpen,
  align = 'end',
}: {
  images: (ImageRef | MediaRef)[]
  localUrls?: Map<string, string>
  onOpen: (img: ImageRef | MediaRef) => void
  align?: 'start' | 'end'
}) {
  return (
    <div className={cn('flex max-w-2xl flex-wrap gap-1.5', align === 'end' ? 'justify-end' : 'justify-start')}>
      {images.map((img) => (
        <AuthedImage
          key={img.id}
          id={img.id}
          mime={img.mime}
          localUrl={localUrls?.get(img.id)}
          onOpen={() => onOpen(img)}
        />
      ))}
    </div>
  )
}

// documentChipIcon picks a chip's icon by mime — PDF, video, audio, or
// plain text/markdown all get a distinct glyph.
function documentChipIcon(mime: string) {
  if (mime.startsWith('video/')) return FileVideoIcon
  if (mime.startsWith('audio/')) return FileMusicIcon
  if (mime === 'text/plain' || mime === 'text/markdown') return File01Icon
  return Pdf02Icon
}

// DocumentChips renders a set of non-image attachments (a user
// message's documents, or an assistant turn's generated media) as
// small clickable chips that open the AttachmentViewer modal — labeled
// by mime, showing the original filename when present.
function DocumentChips({
  documents,
  onOpen,
  align = 'end',
}: {
  documents: (ImageRef | MediaRef)[]
  onOpen: (doc: ImageRef | MediaRef) => void
  align?: 'start' | 'end'
}) {
  return (
    <div className={cn('flex max-w-2xl flex-wrap gap-1.5', align === 'end' ? 'justify-end' : 'justify-start')}>
      {documents.map((doc) => (
        <button
          key={doc.id}
          type="button"
          title={doc.name ?? doc.id.slice(0, 8)}
          onClick={() => onOpen(doc)}
          className="flex items-center gap-1 rounded-lg border border-zinc-950/10 bg-zinc-100 px-2 py-1 text-xs text-zinc-500 transition hover:bg-zinc-200 dark:border-white/10 dark:bg-zinc-800 dark:text-zinc-400 dark:hover:bg-zinc-700"
        >
          <HugeiconsIcon icon={documentChipIcon(doc.mime)} className="size-3.5" />
          {doc.name ?? mimeLabel(doc.mime)}
        </button>
      ))}
    </div>
  )
}

// GeneratedMedia renders an assistant turn's tool-generated media
// (share_file, read_mail_attachment): image mimes as thumbnails via
// AuthedImage, everything else as a chip — same rendering the user
// message's own attachments use, opening the same AttachmentViewer.
function GeneratedMedia({ media }: { media: MediaRef[] }) {
  const [viewerAttachment, setViewerAttachment] = useState<MediaRef | null>(null)
  const images = media.filter((m) => m.mime.startsWith('image/'))
  const documents = media.filter((m) => !m.mime.startsWith('image/'))
  return (
    <div className="flex flex-col items-start gap-1.5">
      {images.length > 0 && (
        <ImageGrid images={images} align="start" onOpen={(img) => setViewerAttachment(img)} />
      )}
      {documents.length > 0 && (
        <DocumentChips documents={documents} align="start" onOpen={(doc) => setViewerAttachment(doc)} />
      )}
      <AttachmentViewer
        open={viewerAttachment !== null}
        onOpenChange={(open) => {
          if (!open) setViewerAttachment(null)
        }}
        attachment={viewerAttachment}
      />
    </div>
  )
}

// SourcesPanel renders a research answer's citations as a distinct,
// clickable list — separate from the prose so "these are the sources"
// reads at a glance instead of blending into the markdown body.
function SourcesPanel({ citations }: { citations: { title: string; url: string }[] }) {
  return (
    <div className="w-full max-w-3xl rounded-lg border border-zinc-200 bg-zinc-50 p-3 dark:border-zinc-700 dark:bg-zinc-900/40">
      <div className="mb-2 flex items-center gap-1.5 text-xs font-medium text-zinc-500 dark:text-zinc-400">
        <HugeiconsIcon icon={Link04Icon} className="size-3.5" />
        Sources
      </div>
      <ol className="space-y-1.5 text-sm">
        {citations.map((c, i) => (
          <li key={i} className="flex gap-2">
            <span className="text-zinc-400 dark:text-zinc-500">{i + 1}.</span>
            <a
              href={c.url}
              target="_blank"
              rel="noopener noreferrer"
              className="truncate text-blue-600 hover:underline dark:text-blue-400"
            >
              {c.title}
            </a>
          </li>
        ))}
      </ol>
    </div>
  )
}

// CopyButton copies a message's raw text; the check confirms briefly.
// By default it only shows on hover of an ancestor "message" group
// (AssistantMessage's wrapper); alwaysVisible drops that dependency
// for contexts with no such group (e.g. inside a collapsible details
// block, already hidden until expanded).
export function CopyButton({
  text,
  label,
  alwaysVisible = false,
}: {
  text: string
  label: string
  alwaysVisible?: boolean
}) {
  const [copied, setCopied] = useState(false)
  const timer = useRef<number | undefined>(undefined)
  useEffect(() => () => window.clearTimeout(timer.current), [])
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text)
      setCopied(true)
      window.clearTimeout(timer.current)
      timer.current = window.setTimeout(() => setCopied(false), 2000)
    } catch {
      // Clipboard unavailable (permissions, insecure context): the
      // button simply does nothing rather than throwing.
    }
  }
  return (
    <button
      type="button"
      aria-label={label}
      data-testid="copy-button"
      data-copied={copied}
      onClick={() => void copy()}
      className={cn(
        'rounded p-1 text-muted-foreground transition hover:bg-accent hover:text-foreground focus-visible:opacity-100',
        alwaysVisible ? 'bg-zinc-100 dark:bg-zinc-800' : 'opacity-0 group-hover/message:opacity-100',
      )}
    >
      <HugeiconsIcon icon={copied ? Tick02Icon : Copy01Icon} className="size-3.5" />
    </button>
  )
}

export function UserMessage({
  text,
  images,
  documents,
  localUrls,
  onRetry,
}: {
  text: string
  // Attached images (transcript's Images, or the live turn's own
  // optimistic list) — thumbnails render above the text bubble.
  images?: ImageRef[]
  // Attached documents (transcript's Documents, or the live turn's own
  // optimistic list) — rendered as small clickable chips.
  documents?: ImageRef[]
  // Optimistic-send local object URLs keyed by attachment id, so a
  // just-sent message's thumbnails render instantly without
  // round-tripping through AuthedImage's authed fetch.
  localUrls?: Map<string, string>
  // Present only for a trailing dangling user message (the turn died
  // before any assistant event landed) — Chat.tsx decides that, this
  // component just renders whatever it's handed.
  onRetry?: () => void
}) {
  const [viewerAttachment, setViewerAttachment] = useState<ImageRef | null>(null)
  const openAttachment = (ref: ImageRef) => setViewerAttachment(ref)
  return (
    <div className="flex flex-col items-end gap-1">
      {images && images.length > 0 && (
        <ImageGrid images={images} localUrls={localUrls} onOpen={openAttachment} />
      )}
      {documents && documents.length > 0 && (
        <DocumentChips documents={documents} onOpen={openAttachment} />
      )}
      <AttachmentViewer
        open={viewerAttachment !== null}
        onOpenChange={(open) => {
          if (!open) setViewerAttachment(null)
        }}
        attachment={viewerAttachment}
        localUrl={viewerAttachment ? localUrls?.get(viewerAttachment.id) : undefined}
      />
      <div className="group/message flex items-end justify-end gap-1">
        <CopyButton text={text} label="Copy message" />
        {text !== '' && (
          <div className="prose prose-sm prose-invert max-w-2xl rounded-2xl bg-blue-600 px-4 py-2.5 text-sm/6 text-white prose-pre:bg-blue-700">
            <ReactMarkdown remarkPlugins={remarkPlugins} rehypePlugins={rehypePlugins}>
              {text}
            </ReactMarkdown>
          </div>
        )}
      </div>
      {onRetry && (
        <div className="flex items-center gap-2 text-muted-foreground">
          <span className="text-xs">No reply, the turn failed.</span>
          <button
            type="button"
            aria-label="Retry"
            data-testid="retry-button"
            onClick={onRetry}
            className="rounded p-1 text-muted-foreground transition hover:bg-accent hover:text-foreground"
          >
            <HugeiconsIcon icon={ReloadIcon} className="size-3.5" />
          </button>
        </div>
      )}
    </div>
  )
}

// CompactionDivider marks where older messages were summarized away
// from the model's context. The UI replay still shows everything above
// it — only the model forgets, and the divider says so.
export function CompactionDivider({ text }: { text: string }) {
  return (
    <div className="flex items-center gap-3" data-testid="compaction-divider">
      <div className="h-px flex-1 bg-zinc-200 dark:bg-zinc-700" />
      <span className="text-xs text-zinc-400 dark:text-zinc-500">{text}</span>
      <div className="h-px flex-1 bg-zinc-200 dark:bg-zinc-700" />
    </div>
  )
}

// InterruptedMessage renders a turn that never completed: the partial
// answer plus an honest marker.
export function InterruptedMessage({ text }: { text: string }) {
  return (
    <div className="group/message flex w-full flex-col items-start gap-2" data-testid="interrupted">
      <div className="prose prose-sm w-full max-w-none dark:prose-invert">
        <ReactMarkdown
          remarkPlugins={remarkPlugins}
          rehypePlugins={rehypePlugins}
          components={{ pre: CodeBlock }}
        >
          {text}
        </ReactMarkdown>
      </div>
      <div className="flex items-center gap-1.5">
        <Badge
          variant="outline"
          className="border-amber-500/40 bg-amber-500/10 text-amber-600 dark:text-amber-400"
        >
          interrupted
        </Badge>
        <CopyButton text={text} label="Copy partial message" />
      </div>
    </div>
  )
}

// ErrorMessage renders a turn that persisted as failed (D-043): a
// terminal error/incomplete with nothing worth keeping, or a completed
// turn with no text, reasoning, or tool calls. Surfacing this IS the
// point — the turn used to vanish from the transcript silently.
export function ErrorMessage({
  text,
  onRetry,
}: {
  text: string
  // Present only for the trailing item when it's safe to retry (same
  // trailing-only condition Chat.tsx applies to user/assistant items) —
  // without this a failed turn had no way back into the UI at all.
  onRetry?: () => void
}) {
  return (
    <div className="flex items-center gap-2" data-testid="turn-failed">
      <Badge variant="destructive">{text || 'this turn failed'}</Badge>
      {onRetry && (
        <button
          type="button"
          aria-label="Retry"
          data-testid="retry-button"
          onClick={onRetry}
          className="rounded p-1 text-muted-foreground transition hover:bg-accent hover:text-foreground"
        >
          <HugeiconsIcon icon={ReloadIcon} className="size-3.5" />
        </button>
      )}
    </div>
  )
}

export function AssistantMessage({
  msg,
  onRetry,
  onShowActivity,
}: {
  msg: AssistantState
  // Present only for the trailing item when it's safe to retry (an
  // error and not mid-stream) — Chat.tsx decides that, this component
  // just renders whatever it's handed.
  onRetry?: () => void
  // Opens the Activity detail panel for this turn — Chat.tsx owns the
  // Sheet and passes this through. Omitted, the activity line simply
  // doesn't render (there's nowhere for it to open).
  onShowActivity?: () => void
}) {
  const tokens = msg.meta?.usage
    ? `${compact(msg.meta.usage.input_tokens)}→${compact(msg.meta.usage.output_tokens)} tok`
    : null
  // Absent on turns persisted before duration tracking shipped — the
  // pill simply doesn't render rather than showing a guessed 0.
  const duration = msg.meta?.durationMs !== undefined ? formatDuration(msg.meta.durationMs) : null
  // null/undefined when the gateway had no price for the serving model
  // (D-013: unknown price is never guessed) — the pill simply omits.
  // When brain converted the billed cost into the user's display
  // currency, that converted figure is the pill's primary text and the
  // billed amount rides the title attr (mission page pattern) —
  // otherwise the pill just shows the billed amount as before.
  const billedCost =
    msg.meta?.cost != null ? money(msg.meta.cost, msg.meta.currency || 'USD') : null
  const cost =
    msg.meta?.convertedCost != null && msg.meta.convertedCurrency
      ? money(msg.meta.convertedCost, msg.meta.convertedCurrency)
      : billedCost
  const costTitle =
    msg.meta?.convertedCost != null && msg.meta.convertedCurrency && billedCost
      ? `Converted from the billed amount (${billedCost}) using a stored exchange rate.`
      : undefined
  // Citations only split out once the answer is done streaming: a
  // partial "## Sources" heading mid-stream would otherwise flicker
  // the body text as more of it arrives.
  const { body, citations } = msg.streaming
    ? { body: msg.text, citations: [] }
    : splitSources(collapseRepeatedTail(msg.text))
  return (
    <div className="group/message flex w-full flex-col items-start gap-2">
      {onShowActivity && <ActivityLine msg={msg} onOpen={onShowActivity} />}

      {msg.permissions.length > 0 && (
        <Badge
          variant="outline"
          className="border-blue-500/40 bg-blue-500/10 text-blue-600 dark:text-blue-400"
          data-testid="awaiting-approval"
        >
          waiting for your approval
        </Badge>
      )}

      <div className="prose prose-sm w-full max-w-none dark:prose-invert">
        <ReactMarkdown
          remarkPlugins={remarkPlugins}
          rehypePlugins={rehypePlugins}
          components={{ pre: CodeBlock }}
        >
          {body}
        </ReactMarkdown>
        {msg.streaming && msg.permissions.length === 0 && <span className="animate-pulse">▍</span>}
      </div>

      {msg.media.length > 0 && <GeneratedMedia media={msg.media} />}

      {citations.length > 0 && <SourcesPanel citations={citations} />}

      {msg.notices.map((n, i) => (
        <Badge
          key={i}
          variant="outline"
          className="border-amber-500/40 bg-amber-500/10 text-amber-600 dark:text-amber-400"
          data-testid="notice"
        >
          {n}
        </Badge>
      ))}
      {msg.error && (
        <div className="flex items-center gap-2">
          <Badge variant="destructive" data-testid="error">
            {msg.error}
          </Badge>
          {onRetry && (
            <button
              type="button"
              aria-label="Retry"
              data-testid="retry-button"
              onClick={onRetry}
              className="rounded p-1 text-muted-foreground transition hover:bg-accent hover:text-foreground"
            >
              <HugeiconsIcon icon={ReloadIcon} className="size-3.5" />
            </button>
          )}
        </div>
      )}
      {!msg.streaming && (Boolean(msg.meta?.provider) || msg.text !== '') && (
        <div className="flex items-center gap-1.5">
          {msg.meta?.provider && (
            <div className="flex gap-1.5" data-testid="meta-badge">
              <ModelBadge provider={msg.meta.provider} model={msg.meta.model ?? ''} />
              {tokens && <Badge variant="secondary">{tokens}</Badge>}
              {duration && (
                <Badge variant="secondary" data-testid="duration-badge">
                  {duration}
                </Badge>
              )}
              {cost && (
                <Badge variant="secondary" data-testid="cost-badge" title={costTitle}>
                  {cost}
                </Badge>
              )}
            </div>
          )}
          {msg.text !== '' && <CopyButton text={msg.text} label="Copy reply" />}
        </div>
      )}
    </div>
  )
}
