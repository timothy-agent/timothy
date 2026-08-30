import {
  FolderZipIcon,
  LibraryIcon,
  Loading03Icon,
  Pdf02Icon,
} from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useEffect, useMemo, useState } from 'react'
import { toast } from 'sonner'
import {
  downloadMissionArchive,
  downloadMissionPdfExport,
  exportMissionPdf,
  getSettings,
  listKbCollections,
  listMissionFiles,
  promoteMissionToKB,
} from '../../api/client'
import type { KbCollection, MediaRef, MissionFile } from '../../api/types'
import { errText } from '../settings/util'
import { Button } from '../ui/button'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '../ui/dialog'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '../ui/tooltip'
import { ArtifactRefChips } from './ArtifactRefsSection'
import { buildFileTree, type FileTreeNode } from './fileTree'
import { FileTreeView } from './FileTreeView'
import { FileViewer } from './FileViewer'
import { FullscreenDialog, FullscreenToggle, useFullscreenPanel } from './FullscreenPanel'

const markdownFileRe = /\.(md|markdown)$/i

// PromoteToKBDialog offers a collection picker and promotes the
// mission's markdown artifacts into it (D-081, issue #370). Only shown
// when the mission is done and has at least one artifact ref (the
// promote endpoint's own gates).
function PromoteToKBDialog({
  missionId,
  open,
  onOpenChange,
}: {
  missionId: string
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const [collections, setCollections] = useState<KbCollection[] | null>(null)
  const [collectionId, setCollectionId] = useState('')
  const [promoting, setPromoting] = useState(false)
  const [promoted, setPromoted] = useState<number | null>(null)

  useEffect(() => {
    if (!open) return
    setPromoted(null)
    listKbCollections()
      .then(setCollections)
      .catch((err: unknown) => toast.error('Could not load collections', { description: errText(err) }))
  }, [open])

  const promote = async () => {
    if (!collectionId) return
    setPromoting(true)
    try {
      const result = await promoteMissionToKB(missionId, collectionId)
      setPromoted(result.promoted)
      if (result.promoted > 0) {
        toast.success(`Promoted ${result.promoted} document${result.promoted === 1 ? '' : 's'} to the knowledge base`)
      } else {
        toast.error('Nothing was promoted', { description: result.failed?.[0] })
      }
    } catch (err) {
      toast.error('Could not promote to knowledge base', { description: errText(err) })
    } finally {
      setPromoting(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Promote to knowledge base</DialogTitle>
        </DialogHeader>
        {promoted !== null ? (
          <p className="text-sm text-muted-foreground">
            Promoted {promoted} document{promoted === 1 ? '' : 's'}. Searchable via search_kb once
            ingestion finishes.
          </p>
        ) : (
          <div className="space-y-1.5">
            <label htmlFor="promote-kb-collection" className="text-sm font-medium">
              Collection
            </label>
            <select
              id="promote-kb-collection"
              value={collectionId}
              onChange={(e) => setCollectionId(e.target.value)}
              className="w-full rounded-md border border-border bg-background px-3 py-2 text-sm"
            >
              <option value="">Select a collection…</option>
              {(collections ?? []).map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}
                </option>
              ))}
            </select>
          </div>
        )}
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {promoted !== null ? 'Close' : 'Cancel'}
          </Button>
          {promoted === null && (
            <Button disabled={!collectionId || promoting} onClick={() => void promote()}>
              {promoting ? 'Promoting…' : 'Promote'}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

export function ArtifactsSection({
  missionId,
  missionName,
  phase,
  workspace,
  refs = [],
}: {
  missionId: string
  missionName?: string
  phase: string
  workspace?: string
  refs?: MediaRef[]
}) {
  const [files, setFiles] = useState<MissionFile[]>([])
  const [truncated, setTruncated] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [selected, setSelected] = useState<MissionFile | undefined>(undefined)
  const [pdfExportEnabled, setPdfExportEnabled] = useState(false)
  const [exportingPdf, setExportingPdf] = useState(false)
  const [promoteOpen, setPromoteOpen] = useState(false)
  const { fullscreen, toggle, close } = useFullscreenPanel()
  const canPromote = phase === 'done' && refs.some((r) => markdownFileRe.test(r.name ?? ''))

  useEffect(() => {
    getSettings()
      .then((s) => setPdfExportEnabled(s.settings.pdf_export_enabled ?? false))
      .catch(() => setPdfExportEnabled(false))
  }, [])

  useEffect(() => {
    if (!workspace) return
    listMissionFiles(missionId).then(
      (r) => {
        setFiles(r.files)
        setTruncated(r.truncated)
        setError(null)
      },
      (err: unknown) => setError(errText(err)),
    )
  }, [missionId, phase, workspace])

  const tree = useMemo(() => buildFileTree(files), [files])

  // No workspace and no refs, or a workspace with nothing in it and no
  // refs (and no fetch error worth surfacing): the whole section
  // disappears rather than showing an empty shell.
  const hasWorkspace = Boolean(workspace)
  if (!hasWorkspace && refs.length === 0) return null
  if (hasWorkspace && files.length === 0 && !error && refs.length === 0) return null

  const downloadAll = () => {
    downloadMissionArchive(missionId).catch((err: unknown) =>
      toast.error('Could not download archive', { description: errText(err) }),
    )
  }

  const hasMarkdown = files.some((f) => markdownFileRe.test(f.path))

  const exportAllPdf = () => {
    setExportingPdf(true)
    exportMissionPdf(missionId)
      .then((r) => downloadMissionPdfExport(r.attachment_id, `${missionName || 'mission'}.pdf`))
      .catch((err: unknown) => toast.error('Could not export PDF', { description: errText(err) }))
      .finally(() => setExportingPdf(false))
  }

  const selectNode = (node: FileTreeNode) => {
    if (node.file) setSelected(node.file)
  }

  // No live workspace: render the refs chips alone, no panel chrome.
  if (!hasWorkspace) {
    return (
      <section>
        <div className="mb-2 flex items-center justify-between">
          <h2 className="text-sm font-semibold tracking-tight">Artifacts</h2>
          {canPromote && (
            <Button variant="outline" size="sm" onClick={() => setPromoteOpen(true)}>
              <HugeiconsIcon icon={LibraryIcon} />
              Promote to KB
            </Button>
          )}
        </div>
        <ArtifactRefChips refs={refs} />
        <PromoteToKBDialog missionId={missionId} open={promoteOpen} onOpenChange={setPromoteOpen} />
      </section>
    )
  }

  const panel = (
    <div
      className={
        fullscreen
          ? 'flex h-full flex-col overflow-hidden rounded-lg border border-border'
          : 'overflow-hidden rounded-lg border border-border'
      }
    >
      <div className="flex items-center justify-between border-b border-border bg-muted/50 px-3 py-1.5">
        <span className="text-xs text-muted-foreground">
          {files.length} file{files.length === 1 ? '' : 's'}
        </span>
        <div className="flex items-center gap-1">
          {canPromote && (
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  variant="ghost"
                  size="icon-sm"
                  aria-label="Promote workspace markdown artifacts to the knowledge base"
                  onClick={() => setPromoteOpen(true)}
                >
                  <HugeiconsIcon icon={LibraryIcon} />
                </Button>
              </TooltipTrigger>
              <TooltipContent>Promote to knowledge base</TooltipContent>
            </Tooltip>
          )}
          {pdfExportEnabled && hasMarkdown && (
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  variant="ghost"
                  size="icon-sm"
                  aria-label="Export all workspace markdown as one merged PDF"
                  onClick={exportAllPdf}
                  disabled={exportingPdf}
                >
                  <HugeiconsIcon
                    icon={exportingPdf ? Loading03Icon : Pdf02Icon}
                    className={exportingPdf ? 'animate-spin' : undefined}
                  />
                </Button>
              </TooltipTrigger>
              <TooltipContent>Export all workspace markdown as one merged PDF</TooltipContent>
            </Tooltip>
          )}
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                variant="ghost"
                size="icon-sm"
                aria-label="Download the workspace as a zip archive"
                onClick={downloadAll}
                disabled={files.length === 0}
              >
                <HugeiconsIcon icon={FolderZipIcon} />
              </Button>
            </TooltipTrigger>
            <TooltipContent>Download the workspace as a zip archive</TooltipContent>
          </Tooltip>
          <FullscreenToggle fullscreen={fullscreen} onToggle={toggle} />
        </div>
      </div>
      {files.length === 0 ? (
        <p className="p-3 text-sm text-muted-foreground">No files yet.</p>
      ) : (
        <div className={fullscreen ? 'flex min-h-0 flex-1' : 'flex h-80'}>
          <div className="w-60 shrink-0 overflow-y-auto border-r border-border">
            <FileTreeView nodes={tree} selectedPath={selected?.path} onSelect={selectNode} />
          </div>
          <div className="min-w-0 flex-1">
            {selected ? (
              <FileViewer missionId={missionId} file={selected} />
            ) : (
              <p className="p-3 text-sm text-muted-foreground">Select a file to preview it.</p>
            )}
          </div>
        </div>
      )}
      {truncated && (
        <p className="border-t border-border px-3 py-1 text-xs text-muted-foreground">
          list truncated
        </p>
      )}
      {error && <p className="px-3 py-1 text-xs text-muted-foreground">{error}</p>}
    </div>
  )

  return (
    <TooltipProvider>
      <section>
        <h2 className="mb-2 text-sm font-semibold tracking-tight">Artifacts</h2>
        {fullscreen ? (
          <FullscreenDialog open={fullscreen} onOpenChange={(o) => !o && close()}>
            {panel}
          </FullscreenDialog>
        ) : (
          panel
        )}
      </section>
      <PromoteToKBDialog missionId={missionId} open={promoteOpen} onOpenChange={setPromoteOpen} />
    </TooltipProvider>
  )
}
