import { Check, Copy, File, FileAudio, FileText, FileVideo, ImageOff, Link, ListTree, RotateCw } from 'lucide-react'
import { useEffect, useRef, useState, type HTMLAttributes } from 'react'
import ReactMarkdown from 'react-markdown'
import { toast } from 'sonner'
import {
  downloadMissionPdfExport,
  exportMessagePDF,
  fetchAttachmentBlob,
  getSettings,
} from '../api/client'
import type { ImageRef, MediaRef } from '../api/types'
import { ApprovalCard, type Decision } from './chat/ApprovalCard'
import { ToolCallGroup } from './chat/ToolCallCard'
import { AttachmentViewer, mimeLabel } from './AttachmentViewer'
import { presetForProviderName, ProviderMark } from './timothy/provider-logo'
import { errText } from '../lib/errors'
import { Badge } from './ui/badge'
import { Button } from './ui/button'
import { Alert, AlertTitle } from './ui/alert'
import { IconButton } from './timothy/icon-button'
import { Spinner } from './timothy/spinner'
import { TooltipProvider } from './ui/tooltip'
import { collapseRepeatedTail, splitSources } from '../lib/citations'
import { attachmentURLCache } from '../lib/attachmentCache'
import type { AssistantState } from '../lib/chat'
import { compact, formatDuration, money } from '../lib/format'
import { markdownComponents, rehypePlugins, remarkPlugins } from '../lib/markdown'
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
        className="flex size-full min-h-24 items-center justify-center rounded-md bg-muted text-muted-foreground"
        title={id}
      >
        <ImageOff className="size-6" aria-hidden />
      </div>
    )
  }

  if (!url) {
    return (
      <div
        data-testid="attachment-loading"
        className="flex size-full min-h-24 items-center justify-center rounded-md bg-muted"
      >
        <Spinner size="sm" />
      </div>
    )
  }

  return (
    <img
      src={url}
      alt={mime}
      onClick={onOpen}
      className="max-h-50 max-w-full cursor-pointer rounded-md object-cover"
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
  if (mime.startsWith('video/')) return FileVideo
  if (mime.startsWith('audio/')) return FileAudio
  if (mime === 'text/plain' || mime === 'text/markdown') return File
  return FileText
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
      {documents.map((doc) => {
        const Icon = documentChipIcon(doc.mime)
        return (
          <button
            key={doc.id}
            type="button"
            title={doc.name ?? doc.id.slice(0, 8)}
            onClick={() => onOpen(doc)}
            className="min-w-0 max-w-full"
          >
            <Badge variant="secondary" className="min-w-0 cursor-pointer gap-1 break-words hover:bg-secondary/80">
              <Icon className="size-3.5 shrink-0" aria-hidden />
              {doc.name ?? mimeLabel(doc.mime)}
            </Badge>
          </button>
        )
      })}
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
    <div className="mt-3 w-full min-w-0 max-w-3xl rounded-md border border-border bg-card p-3">
      <div className="mb-2 flex items-center gap-1.5 text-xs font-medium uppercase tracking-[0.04em] text-muted-foreground">
        <Link className="size-3.5" aria-hidden />
        Sources
      </div>
      <ol className="space-y-1.5 text-sm">
        {citations.map((c, i) => (
          <li key={i} className="flex min-w-0 gap-2">
            <span className="text-muted-foreground">{i + 1}.</span>
            <a
              href={c.url}
              target="_blank"
              rel="noopener noreferrer"
              className="min-w-0 break-all text-foreground underline-offset-2 hover:underline"
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
// A thin wrapper around the timothy CopyButton's copy/revert logic and
// icons, adapted to the old text/label/alwaysVisible prop names other
// files still call it with, and keeping the data-testid/data-copied
// hooks callers' tests rely on (the shared timothy component exposes
// neither). By default it only shows on hover of an ancestor "message"
// group (AssistantMessage's wrapper); alwaysVisible drops that
// dependency for contexts with no such group (e.g. inside a
// collapsible details block, already hidden until expanded).
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
        'inline-flex size-7 items-center justify-center rounded-md text-muted-foreground transition hover:bg-accent hover:text-foreground focus-visible:opacity-100',
        !alwaysVisible && 'opacity-0 group-hover/message:opacity-100',
      )}
    >
      {copied ? <Check className="size-3.5" aria-hidden /> : <Copy className="size-3.5" aria-hidden />}
    </button>
  )
}

// MetaItem is one dot-separated entry in the turn footer's metadata line.
function MetaItem({ children, ...props }: HTMLAttributes<HTMLSpanElement>) {
  return (
    <>
      <span aria-hidden className="select-none">
        ·
      </span>
      <span {...props}>{children}</span>
    </>
  )
}

// ExportPDFButton renders a completed assistant message's markdown as
// a typeset PDF via the pdfgen sidecar and downloads it. Only mounted
// when pdf_export_enabled — the caller decides that, this component
// just renders the button.
function ExportPDFButton({ text }: { text: string }) {
  const [exporting, setExporting] = useState(false)
  const exportPdf = () => {
    setExporting(true)
    exportMessagePDF('Message', text)
      .then((r) => downloadMissionPdfExport(r.attachment_id, 'message.pdf'))
      .catch((err: unknown) => toast.error('Could not export PDF', { description: errText(err) }))
      .finally(() => setExporting(false))
  }
  return (
    <TooltipProvider>
      <IconButton
        label="Export PDF"
        data-testid="export-pdf-button"
        icon={FileText}
        loading={exporting}
        variant="ghost"
        size="xs"
        onClick={exportPdf}
      />
    </TooltipProvider>
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
    <div className="flex w-full min-w-0 flex-col items-end gap-1">
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
      <div className="group/message flex w-full min-w-0 items-end justify-end gap-1">
        <CopyButton text={text} label="Copy message" />
        {text !== '' && (
          <div className="prose ml-auto min-w-0 max-w-[85%] break-words rounded-md bg-muted px-4 py-3 text-prose text-foreground [overflow-wrap:anywhere] dark:prose-invert">
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
            className="rounded-md p-1 text-muted-foreground transition hover:bg-accent hover:text-foreground"
          >
            <RotateCw className="size-3.5" aria-hidden />
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
    <div className="flex items-center gap-3 text-xs text-muted-foreground" data-testid="compaction-divider">
      <div className="h-px flex-1 bg-border" />
      <span>{text}</span>
      <div className="h-px flex-1 bg-border" />
    </div>
  )
}

// InterruptedMessage renders a turn that never completed: the partial
// answer plus an honest marker. Neutral row (contract 14.4:
// interruptions/reconnects are neutral, never amber warnings).
export function InterruptedMessage({ text }: { text: string }) {
  return (
    <div
      className="group/message flex w-full flex-col gap-2 rounded-md border border-border bg-muted/40 px-4 py-3 text-sm text-muted-foreground"
      data-testid="interrupted"
    >
      <div className="prose min-w-0 max-w-none break-words text-foreground [overflow-wrap:anywhere] dark:prose-invert">
        <ReactMarkdown
          remarkPlugins={remarkPlugins}
          rehypePlugins={rehypePlugins}
          components={markdownComponents}
        >
          {text}
        </ReactMarkdown>
      </div>
      <div className="flex min-w-0 items-center gap-1.5">
        <Badge variant="neutral">interrupted</Badge>
        <CopyButton text={text} label="Copy partial message" alwaysVisible />
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
    <Alert tone="destructive" data-testid="turn-failed">
      <AlertTitle>This turn failed</AlertTitle>
      <div className="flex min-w-0 items-center gap-2">
        <p className="min-w-0 break-words font-mono text-xs [overflow-wrap:anywhere]">{text || 'this turn failed'}</p>
        {onRetry && (
          <Button variant="outline" size="sm" data-testid="retry-button" onClick={onRetry}>
            <RotateCw aria-hidden />
            Retry
          </Button>
        )}
      </div>
    </Alert>
  )
}

export function AssistantMessage({
  msg,
  onRetry,
  onShowActivity,
  onDecision,
}: {
  msg: AssistantState
  // Present only for the trailing item when it's safe to retry (an
  // error and not mid-stream) — Chat.tsx decides that, this component
  // just renders whatever it's handed.
  onRetry?: () => void
  // Opens the Activity detail panel for this turn — Chat.tsx owns the
  // Sheet and passes this through. Omitted, the Activity button simply
  // doesn't render (there's nowhere for it to open).
  onShowActivity?: () => void
  // Answers a pending permission request inline. Omitted, pending
  // permissions render nothing (Chat.tsx's ApprovalDialog is the only
  // way to decide them in that case).
  onDecision?: (id: string, d: Decision) => void
}) {
  const [pdfExportEnabled, setPdfExportEnabled] = useState(false)
  useEffect(() => {
    getSettings()
      .then((s) => setPdfExportEnabled(s.settings.pdf_export_enabled ?? false))
      .catch(() => setPdfExportEnabled(false))
  }, [])
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
    <div className="group/message flex w-full min-w-0 flex-col items-start">
      <div className="flex min-w-0 max-w-full items-center gap-2.5">
        <span
          aria-hidden
          className="inline-flex size-5 shrink-0 items-center justify-center rounded-md bg-brand text-xs font-semibold text-brand-foreground"
        >
          T
        </span>
      </div>

      {msg.tools.length > 0 && (
        <div className="mt-3 w-full min-w-0">
          <ToolCallGroup runs={msg.tools} defaultOpen={msg.tools.some((t) => t.status === 'running')} />
        </div>
      )}

      {onDecision &&
        msg.permissions.map((p) => (
          <ApprovalCard key={p.id} id={`approval-${p.id}`} request={p} onDecision={onDecision} className="mt-4" />
        ))}

      <div
        className={cn(
          'prose w-full min-w-0 max-w-none break-words [overflow-wrap:anywhere] dark:prose-invert',
          msg.tools.length > 0 ? 'mt-3' : 'mt-2',
        )}
      >
        <ReactMarkdown
          remarkPlugins={remarkPlugins}
          rehypePlugins={rehypePlugins}
          components={markdownComponents}
        >
          {body}
        </ReactMarkdown>
        {msg.streaming && msg.permissions.length === 0 && <span className="animate-pulse">▍</span>}
      </div>

      {msg.media.length > 0 && (
        <div className="mt-3">
          <GeneratedMedia media={msg.media} />
        </div>
      )}

      {citations.length > 0 && (
        <div className="mt-3">
          <SourcesPanel citations={citations} />
        </div>
      )}

      {msg.notices.map((n, i) => (
        <Badge key={i} variant="warning" data-testid="notice" className={i === 0 ? 'mt-3' : undefined}>
          {n}
        </Badge>
      ))}
      {msg.error && (
        <div className="mt-3 flex items-center gap-2">
          <Badge variant="destructive" data-testid="error">
            {msg.error}
          </Badge>
          {onRetry && (
            <Button variant="outline" size="sm" data-testid="retry-button" onClick={onRetry}>
              <RotateCw aria-hidden />
              Retry
            </Button>
          )}
        </div>
      )}
      {!msg.streaming && (Boolean(msg.meta?.provider) || msg.text !== '' || Boolean(onShowActivity)) && (
        <div className="mt-2 flex min-w-0 max-w-full flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
          {msg.meta?.provider && (
            <div className="flex min-w-0 flex-wrap items-center gap-x-2 tabular-nums" data-testid="meta-badge">
              <span className="inline-flex items-center gap-1.5">
                <ProviderMark preset={presetForProviderName(msg.meta.provider)} className="size-3" />
                {msg.meta.model ?? ''}
              </span>
              {tokens && <MetaItem>{tokens}</MetaItem>}
              {duration && <MetaItem data-testid="duration-badge">{duration}</MetaItem>}
              {cost && (
                <MetaItem data-testid="cost-badge" title={costTitle}>
                  {cost}
                </MetaItem>
              )}
            </div>
          )}
          <div className="ml-auto flex items-center gap-0.5">
            {msg.text !== '' && <CopyButton text={msg.text} label="Copy reply" alwaysVisible />}
            {msg.text !== '' && pdfExportEnabled && <ExportPDFButton text={msg.text} />}
            {onShowActivity && (
              <Button variant="ghost" size="xs" data-testid="show-activity" onClick={onShowActivity}>
                <ListTree aria-hidden />
                Activity
              </Button>
            )}
          </div>
        </div>
      )}
    </div>
  )
}
