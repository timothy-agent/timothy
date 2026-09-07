import { BookOpen, FileDown, FolderArchive, FolderOpen } from 'lucide-react'
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
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../ui/select'
import { TooltipProvider } from '../ui/tooltip'
import { EmptyState } from '../timothy/empty-state'
import { Field } from '../timothy/field'
import { IconButton } from '../timothy/icon-button'
import { Panel } from '../timothy/panel'
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
          <Field label="Collection" htmlFor="promote-kb-collection">
            {(controlProps) => (
              <Select value={collectionId} onValueChange={setCollectionId}>
                <SelectTrigger id={controlProps.id} className="w-full" aria-describedby={controlProps['aria-describedby']}>
                  <SelectValue placeholder="Select a collection…" />
                </SelectTrigger>
                <SelectContent>
                  {(collections ?? []).map((c) => (
                    <SelectItem key={c.id} value={c.id}>
                      {c.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}
          </Field>
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
      <TooltipProvider>
        <Panel
          title="Files"
          density="operational"
          actions={canPromote ? <Button variant="outline" size="sm" onClick={() => setPromoteOpen(true)}>Promote to KB</Button> : undefined}
        >
          <div className="p-3">
            <ArtifactRefChips refs={refs} />
          </div>
        </Panel>
        <PromoteToKBDialog missionId={missionId} open={promoteOpen} onOpenChange={setPromoteOpen} />
      </TooltipProvider>
    )
  }

  const actions = (
    <>
      <span className="mr-auto text-xs text-muted-foreground">
        {files.length} file{files.length === 1 ? '' : 's'}
      </span>
      {canPromote && (
        <IconButton
          size="sm"
          label="Promote workspace markdown artifacts to the knowledge base"
          icon={BookOpen}
          onClick={() => setPromoteOpen(true)}
        />
      )}
      {pdfExportEnabled && hasMarkdown && (
        <IconButton
          size="sm"
          label="Export all workspace markdown as one merged PDF"
          icon={FileDown}
          onClick={exportAllPdf}
          loading={exportingPdf}
        />
      )}
      <IconButton
        size="sm"
        label="Download the workspace as a zip archive"
        icon={FolderArchive}
        onClick={downloadAll}
        disabled={files.length === 0}
      />
      <FullscreenToggle fullscreen={fullscreen} onToggle={toggle} />
    </>
  )

  const body = (
    <div className={fullscreen ? 'flex h-full flex-col' : undefined}>
      {files.length === 0 ? (
        <EmptyState density="operational" icon={FolderOpen} title="No files yet." />
      ) : (
        <div className={fullscreen ? 'flex min-h-0 flex-1' : 'flex h-80'}>
          <div className="w-60 shrink-0 overflow-y-auto border-r border-border">
            <FileTreeView nodes={tree} selectedPath={selected?.path} onSelect={selectNode} />
          </div>
          <div className="min-w-0 flex-1">
            {selected ? (
              <FileViewer missionId={missionId} file={selected} />
            ) : (
              <p className="flex h-full items-center justify-center p-3 text-center text-sm text-muted-foreground">
                Select a file to preview it.
              </p>
            )}
          </div>
        </div>
      )}
      {truncated && (
        <p className="border-t border-border px-3 py-1.5 text-xs text-muted-foreground">list truncated</p>
      )}
      {error && <p className="border-t border-border px-3 py-1.5 text-xs text-destructive">{error}</p>}
    </div>
  )

  const panel = (
    <Panel title="Files" density="operational" actions={actions} className={fullscreen ? 'flex h-full flex-col' : undefined}>
      {body}
    </Panel>
  )

  return (
    <TooltipProvider>
      {fullscreen ? (
        <FullscreenDialog open={fullscreen} onOpenChange={(o) => !o && close()}>
          {panel}
        </FullscreenDialog>
      ) : (
        panel
      )}
      <PromoteToKBDialog missionId={missionId} open={promoteOpen} onOpenChange={setPromoteOpen} />
    </TooltipProvider>
  )
}
