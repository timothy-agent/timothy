import {
  Download04Icon,
  EyeIcon,
  LinkSquare01Icon,
  Loading03Icon,
  Pdf02Icon,
  SourceCodeIcon,
} from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
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
import { FileCodeBlock, FileMarkdownBlock } from '../FilePreviewBlocks'
import { CopyButton } from '../Message'
import { errText } from '../settings/util'
import { Button } from '../ui/button'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '../ui/tooltip'
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
        <div className="flex items-center justify-between gap-3 border-b border-border bg-muted/50 px-3 py-1.5">
          <div className="min-w-0 flex-1">
            <p className="truncate font-mono text-xs">{file.path}</p>
            <p className="text-xs text-muted-foreground">
              {lineCount != null && <span>{lineCount} lines · </span>}
              <span>{humanSize(file.size)}</span>
            </p>
          </div>
          <div className="flex items-center gap-1.5">
            {kind === 'markdown' && state.status === 'text' && (
              <Tooltip>
                <TooltipTrigger asChild>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    aria-label={showRawMarkdown ? 'Show rendered markdown' : 'Show raw markdown source'}
                    onClick={() => setShowRawMarkdown((v) => !v)}
                  >
                    <HugeiconsIcon icon={showRawMarkdown ? EyeIcon : SourceCodeIcon} />
                  </Button>
                </TooltipTrigger>
                <TooltipContent>
                  {showRawMarkdown ? 'Show rendered markdown' : 'Show raw markdown source'}
                </TooltipContent>
              </Tooltip>
            )}
            {state.status === 'text' && (
              <>
                <Tooltip>
                  <TooltipTrigger asChild>
                    <span className="inline-flex">
                      <CopyButton text={state.text} label={`Copy ${file.path}`} alwaysVisible />
                    </span>
                  </TooltipTrigger>
                  <TooltipContent>Copy</TooltipContent>
                </Tooltip>
                <Tooltip>
                  <TooltipTrigger asChild>
                    <Button variant="ghost" size="icon-sm" aria-label="Open raw content in a new tab" onClick={openRaw}>
                      <HugeiconsIcon icon={LinkSquare01Icon} />
                    </Button>
                  </TooltipTrigger>
                  <TooltipContent>Open raw content in a new tab</TooltipContent>
                </Tooltip>
              </>
            )}
            {(state.status === 'image' || state.status === 'pdf') && (
              <Tooltip>
                <TooltipTrigger asChild>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    aria-label="Open raw file in a new tab"
                    onClick={() => window.open(state.url, '_blank')}
                  >
                    <HugeiconsIcon icon={LinkSquare01Icon} />
                  </Button>
                </TooltipTrigger>
                <TooltipContent>Open raw file in a new tab</TooltipContent>
              </Tooltip>
            )}
            {kind === 'markdown' && pdfExportEnabled && (
              <Tooltip>
                <TooltipTrigger asChild>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    aria-label="Export this file as a typeset PDF"
                    onClick={exportPdf}
                    disabled={exportingPdf}
                  >
                    <HugeiconsIcon
                      icon={exportingPdf ? Loading03Icon : Pdf02Icon}
                      className={exportingPdf ? 'animate-spin' : undefined}
                    />
                  </Button>
                </TooltipTrigger>
                <TooltipContent>Export this file as a typeset PDF</TooltipContent>
              </Tooltip>
            )}
            <Tooltip>
              <TooltipTrigger asChild>
                <Button variant="ghost" size="icon-sm" aria-label="Download this file" onClick={download}>
                  <HugeiconsIcon icon={Download04Icon} />
                </Button>
              </TooltipTrigger>
              <TooltipContent>Download this file</TooltipContent>
            </Tooltip>
          </div>
        </div>
        <div className="min-h-0 flex-1 overflow-auto">
          {state.status === 'loading' && (
            <p className="p-3 text-sm text-muted-foreground">Loading…</p>
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
