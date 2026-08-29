import { Download04Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useEffect, useState } from 'react'
import {
  MissionFileTooLargeError,
  downloadMissionFile,
  fetchMissionFileBlob,
  missionFilePreviewCap,
  missionPdfPreviewCap,
} from '../../api/client'
import type { MissionFile } from '../../api/types'
import { FileCodeBlock, FileMarkdownBlock } from '../FilePreviewBlocks'
import { CopyButton } from '../Message'
import { errText } from '../settings/util'
import { Button } from '../ui/button'
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
  const kind = previewKindOf(file.path)

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
            <Button variant="outline" size="sm" onClick={() => setShowRawMarkdown((v) => !v)}>
              {showRawMarkdown ? 'Rendered' : 'Source'}
            </Button>
          )}
          {state.status === 'text' && (
            <>
              <CopyButton text={state.text} label={`Copy ${file.path}`} alwaysVisible />
              <Button variant="ghost" size="sm" onClick={openRaw}>
                Raw
              </Button>
            </>
          )}
          {(state.status === 'image' || state.status === 'pdf') && (
            <Button variant="ghost" size="sm" onClick={() => window.open(state.url, '_blank')}>
              Raw
            </Button>
          )}
          <Button variant="ghost" size="sm" onClick={download}>
            <HugeiconsIcon icon={Download04Icon} />
            Download
          </Button>
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
  )
}
