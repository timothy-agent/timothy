import { FolderZipIcon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useEffect, useMemo, useState } from 'react'
import { toast } from 'sonner'
import { downloadMissionArchive, listMissionFiles } from '../../api/client'
import type { MediaRef, MissionFile } from '../../api/types'
import { errText } from '../settings/util'
import { Button } from '../ui/button'
import { ArtifactRefChips } from './ArtifactRefsSection'
import { buildFileTree, type FileTreeNode } from './fileTree'
import { FileTreeView } from './FileTreeView'
import { FileViewer } from './FileViewer'
import { FullscreenDialog, FullscreenToggle, useFullscreenPanel } from './FullscreenPanel'

export function ArtifactsSection({
  missionId,
  phase,
  workspace,
  refs = [],
}: {
  missionId: string
  phase: string
  workspace?: string
  refs?: MediaRef[]
}) {
  const [files, setFiles] = useState<MissionFile[]>([])
  const [truncated, setTruncated] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [selected, setSelected] = useState<MissionFile | undefined>(undefined)
  const { fullscreen, toggle, close } = useFullscreenPanel()

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

  const selectNode = (node: FileTreeNode) => {
    if (node.file) setSelected(node.file)
  }

  // No live workspace: render the refs chips alone, no panel chrome.
  if (!hasWorkspace) {
    return (
      <section>
        <h2 className="mb-2 text-sm font-semibold tracking-tight">Artifacts</h2>
        <ArtifactRefChips refs={refs} />
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
          <Button variant="ghost" size="sm" onClick={downloadAll} disabled={files.length === 0}>
            <HugeiconsIcon icon={FolderZipIcon} />
            Download all
          </Button>
          <FullscreenToggle fullscreen={fullscreen} onToggle={toggle} />
        </div>
      </div>
      {refs.length > 0 && (
        <div className="border-b border-border px-3 py-2">
          <ArtifactRefChips refs={refs} />
        </div>
      )}
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
  )
}
