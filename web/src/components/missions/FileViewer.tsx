import { Code, Download, ExternalLink, Eye, FileDown } from 'lucide-react'
import { useEffect, useState } from 'react'
import { toast } from 'sonner'
import {
  MissionFileTooLargeError,
  downloadMissionFile,
  downloadMissionPdfExport,
  exportMissionPdf,
  fetchMissionFileBlob,
  getSettings,
  missionFilePreviewCap,
  missionPdfPreviewCap,
} from '../../api/client'
import type { MissionFile } from '../../api/types'
import { IconButton } from '../timothy/icon-button'
import { Spinner } from '../timothy/spinner'
import { FileCodeBlock, FileMarkdownBlock } from '../FilePreviewBlocks'
import { CopyButton } from '../Message'
import { errText } from '../settings/util'
import { TooltipProvider } from '../ui/tooltip'
import { previewKindOf } from './filePreviewKind'

function humanSize(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}

type LoadState =
  | { status: 'loading' }
  | { status: 'error'; message: string }
  | { status: 'too-large' }
  | { status: 'text'; text: string }
  | { status: 'image'; url: string }
  | { status: 'pdf'; url: string }

export function FileViewer({ missionId, file }: { missionId: string; file: MissionFile }) {
  const [state, setState] = useState<LoadState>({ status: 'loading' })
  const [showRawMarkdown, setShowRawMarkdown] = useState(false)
  const [pdfExportEnabled, setPdfExportEnabled] = useState(false)
  const [exportingPdf, setExportingPdf] = useState(false)
  const kind = previewKindOf(file.path)

  useEffect(() => {
    getSettings()
      .then((s) => setPdfExportEnabled(s.settings.pdf_export_enabled ?? false))
      .catch(() => setPdfExportEnabled(false))
  }, [])

  useEffect(() => {
    setState({ status: 'loading' })
    setShowRawMarkdown(false)
    if (kind === 'unsupported') {
      setState({ status: 'error', message: 'unsupported' })
      return
    }
    let objectUrl: string | undefined
    let cancelled = false
    const cap = kind === 'pdf' ? missionPdfPreviewCap : missionFilePreviewCap
    fetchMissionFileBlob(missionId, file.path, cap).then(
      (blob) => {
        if (cancelled) return
        if (kind === 'image') {
          objectUrl = URL.createObjectURL(blob)
          setState({ status: 'image', url: objectUrl })
        } else if (kind === 'pdf') {
          // Server forces Content-Type: application/octet-stream on this
          // route deliberately — retype the blob client-side so the
          // iframe's PDF plugin picks it up (see FileViewer.tsx's guard).
          objectUrl = URL.createObjectURL(new Blob([blob], { type: 'application/pdf' }))
          setState({ status: 'pdf', url: objectUrl })
        } else {
          blob.text().then((text) => {
            if (!cancelled) setState({ status: 'text', text })
          })
        }
      },
      (err: unknown) => {
        if (cancelled) return
        if (err instanceof MissionFileTooLargeError) {
          setState({ status: 'too-large' })
        } else {
          setState({ status: 'error', message: errText(err) })
        }
      },
    )
    return () => {
      cancelled = true
      if (objectUrl) URL.revokeObjectURL(objectUrl)
    }
  }, [missionId, file.path, kind])

  const download = () => {
    downloadMissionFile(missionId, file.path).catch((err: unknown) =>
      setState({ status: 'error', message: errText(err) }),
    )
  }

  const exportPdf = () => {
    setExportingPdf(true)
    const base = file.path.split('/').pop() || file.path
    const name = base.includes('.') ? base.slice(0, base.lastIndexOf('.')) : base
    exportMissionPdf(missionId, file.path)
      .then((r) => downloadMissionPdfExport(r.attachment_id, `${name}.pdf`))
      .catch((err: unknown) => toast.error('Could not export PDF', { description: errText(err) }))
      .finally(() => setExportingPdf(false))
  }

  // openRaw opens the already-fetched content in a new tab via a Blob
  // URL — fetchMissionFileBlob requires an Authorization header the
  // server never accepts from a plain navigation, so a direct href to
  // the files/* route won't authenticate. Revoked shortly after open:
  // the new tab has already read the bytes by the time it'd matter.
  const openRaw = () => {
    if (state.status !== 'text') return
    const url = URL.createObjectURL(new Blob([state.text], { type: 'text/plain' }))
    window.open(url, '_blank')
    setTimeout(() => URL.revokeObjectURL(url), 60_000)
  }

  const lineCount = state.status === 'text' ? state.text.split('\n').length : undefined

  return (
    <TooltipProvider>
      <div className="flex h-full min-w-0 flex-col">
        <div className="flex h-9 items-center justify-between gap-3 border-b border-border px-3">
          <div className="min-w-0 flex-1">
            <p className="truncate font-mono text-xs">{file.path}</p>
          </div>
          <p className="shrink-0 text-xs tabular-nums text-muted-foreground">
            {lineCount != null && <span>{lineCount} lines · </span>}
            <span>{humanSize(file.size)}</span>
          </p>
          <div className="flex shrink-0 items-center gap-1">
            {kind === 'markdown' && state.status === 'text' && (
              <IconButton
                size="sm"
                label={showRawMarkdown ? 'Show rendered markdown' : 'Show raw markdown source'}
                icon={showRawMarkdown ? Eye : Code}
                onClick={() => setShowRawMarkdown((v) => !v)}
              />
            )}
            {state.status === 'text' && (
              <>
                <CopyButton text={state.text} label={`Copy ${file.path}`} alwaysVisible />
                <IconButton size="sm" label="Open raw content in a new tab" icon={ExternalLink} onClick={openRaw} />
              </>
            )}
            {(state.status === 'image' || state.status === 'pdf') && (
              <IconButton
                size="sm"
                label="Open raw file in a new tab"
                icon={ExternalLink}
                onClick={() => window.open(state.url, '_blank')}
              />
            )}
            {kind === 'markdown' && pdfExportEnabled && (
              <IconButton
                size="sm"
                label="Export this file as a typeset PDF"
                icon={FileDown}
                onClick={exportPdf}
                loading={exportingPdf}
              />
            )}
            <IconButton size="sm" label="Download this file" icon={Download} onClick={download} />
          </div>
        </div>
        <div className="min-h-0 flex-1 overflow-auto">
          {state.status === 'loading' && (
            <div className="flex justify-center p-3">
              <Spinner />
            </div>
          )}
          {state.status === 'too-large' && (
            <p className="p-3 text-sm text-muted-foreground">
              File is larger than {humanSize(kind === 'pdf' ? missionPdfPreviewCap : missionFilePreviewCap)},
              too large to preview. Download it instead.
            </p>
          )}
          {state.status === 'error' && (
            <p className="p-3 text-sm text-muted-foreground">
              {state.message === 'unsupported'
                ? "Can't preview this file type. Download it instead."
                : state.message}
            </p>
          )}
          {state.status === 'image' && (
            <div className="flex justify-center p-3">
              <img src={state.url} alt={file.path} className="max-w-full" />
            </div>
          )}
          {state.status === 'pdf' && (
            <iframe src={state.url} title={file.path} className="size-full border-0" />
          )}
          {state.status === 'text' && kind === 'markdown' && (
            <FileMarkdownBlock text={state.text} raw={showRawMarkdown} />
          )}
          {state.status === 'text' && kind === 'code' && (
            <FileCodeBlock code={state.text} path={file.path} />
          )}
        </div>
      </div>
    </TooltipProvider>
  )
}
