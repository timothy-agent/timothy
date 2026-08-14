import {
  ArrowLeft01Icon,
  CancelCircleIcon,
  CloudUploadIcon,
  Delete02Icon,
  File02Icon,
  Loading03Icon,
  ReloadIcon,
  Tick02Icon,
} from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useCallback, useEffect, useRef, useState } from 'react'
import { Link, Navigate, useNavigate, useParams } from 'react-router'
import { toast } from 'sonner'
import {
  addKbDocumentFromUrl,
  deleteKbCollection,
  deleteKbDocument,
  getKbCollection,
  listKbDocuments,
  reingestKbDocument,
  uploadKbDocument,
} from '../../api/client'
import type { KbCollection, KbDocument } from '../../api/types'
import { humanBytes, relativeTime } from '../../lib/format'
import { Button } from '../ui/button'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '../ui/dialog'
import { errText } from './util'

const acceptExt = '.pdf,.md,.txt,.docx,.html'

const statusStyle: Record<KbDocument['status'], string> = {
  pending: 'bg-muted text-muted-foreground',
  ingesting: 'bg-blue-100 text-blue-800 dark:bg-blue-950 dark:text-blue-300',
  ready: 'bg-green-100 text-green-800 dark:bg-green-950 dark:text-green-300',
  failed: 'bg-red-100 text-red-800 dark:bg-red-950 dark:text-red-300',
}

function StatusBadge({ doc }: { doc: KbDocument }) {
  const icon =
    doc.status === 'ingesting' ? (
      <HugeiconsIcon icon={Loading03Icon} className="size-3 animate-spin" />
    ) : doc.status === 'ready' ? (
      <HugeiconsIcon icon={Tick02Icon} className="size-3" />
    ) : doc.status === 'failed' ? (
      <HugeiconsIcon icon={CancelCircleIcon} className="size-3" />
    ) : null

  return (
    <span
      title={doc.status === 'failed' ? doc.error : undefined}
      className={`inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium ${statusStyle[doc.status]}`}
    >
      {icon}
      {doc.status}
    </span>
  )
}

// pending/ingesting documents are polled every 3s until every
// document in the collection reaches a terminal state.
const pollMs = 3000

export function KnowledgeCollectionDetail() {
  const { id } = useParams()
  const navigate = useNavigate()
  const [collection, setCollection] = useState<KbCollection | null | undefined>(undefined)
  const [documents, setDocuments] = useState<KbDocument[]>([])
  const [confirmDeleteCollection, setConfirmDeleteCollection] = useState(false)
  const [confirmDeleteDoc, setConfirmDeleteDoc] = useState<KbDocument | null>(null)
  const [uploading, setUploading] = useState<Record<string, boolean>>({})
  const [url, setUrl] = useState('')
  const [addingUrl, setAddingUrl] = useState(false)
  const inputRef = useRef<HTMLInputElement>(null)

  const refresh = useCallback(() => {
    if (!id) return
    Promise.all([getKbCollection(id), listKbDocuments(id)])
      .then(([c, docs]) => {
        setCollection(c)
        setDocuments(docs)
      })
      .catch((err: unknown) => {
        toast.error('Could not load collection', { description: errText(err) })
        setCollection(null)
      })
  }, [id])
  useEffect(refresh, [refresh])

  const hasPending = documents.some((d) => d.status === 'pending' || d.status === 'ingesting')
  useEffect(() => {
    if (!hasPending || !id) return
    const t = setInterval(() => {
      listKbDocuments(id).then(setDocuments, () => undefined)
    }, pollMs)
    return () => clearInterval(t)
  }, [hasPending, id])

  if (collection === null) return <Navigate to="/settings/knowledge" replace />
  if (collection === undefined) return null

  async function uploadFiles(files: File[]) {
    if (!id) return
    for (const file of files) {
      const key = crypto.randomUUID()
      setUploading((prev) => ({ ...prev, [key]: true }))
      try {
        const doc = await uploadKbDocument(id, file)
        setDocuments((prev) => [doc, ...prev])
      } catch (err) {
        toast.error(`${file.name}: upload failed`, { description: errText(err) })
      } finally {
        setUploading((prev) => {
          const next = { ...prev }
          delete next[key]
          return next
        })
      }
    }
  }

  const addUrl = async () => {
    if (!id || !url.trim() || addingUrl) return
    setAddingUrl(true)
    try {
      const doc = await addKbDocumentFromUrl(id, url.trim())
      setDocuments((prev) => [doc, ...prev])
      setUrl('')
    } catch (err) {
      toast.error('Could not add URL', { description: errText(err) })
    } finally {
      setAddingUrl(false)
    }
  }

  const removeCollection = async () => {
    if (!id) return
    try {
      await deleteKbCollection(id)
      toast.success('Collection removed')
      navigate('/settings/knowledge')
    } catch (err) {
      toast.error('Could not remove collection', { description: errText(err) })
      setConfirmDeleteCollection(false)
    }
  }

  const removeDoc = async () => {
    if (!confirmDeleteDoc) return
    try {
      await deleteKbDocument(confirmDeleteDoc.id)
      setDocuments((prev) => prev.filter((d) => d.id !== confirmDeleteDoc.id))
      toast.success('Document removed')
    } catch (err) {
      toast.error('Could not remove document', { description: errText(err) })
    } finally {
      setConfirmDeleteDoc(null)
    }
  }

  const reingest = async (doc: KbDocument) => {
    try {
      await reingestKbDocument(doc.id)
      setDocuments((prev) =>
        prev.map((d) => (d.id === doc.id ? { ...d, status: 'pending', error: '' } : d)),
      )
    } catch (err) {
      toast.error('Could not re-ingest document', { description: errText(err) })
    }
  }

  return (
    <div className="mt-6 w-full space-y-6">
      <Link
        to="/settings/knowledge"
        className="inline-flex w-fit items-center gap-1.5 text-sm text-muted-foreground transition hover:text-foreground"
      >
        <HugeiconsIcon icon={ArrowLeft01Icon} className="size-4" />
        Knowledge
      </Link>

      <div className="flex items-center justify-between border-b border-border pb-6">
        <div>
          <h1 className="text-xl font-semibold tracking-tight">{collection.name}</h1>
          {collection.description && (
            <p className="text-sm text-muted-foreground">{collection.description}</p>
          )}
        </div>
        <Button variant="destructive" onClick={() => setConfirmDeleteCollection(true)}>
          <HugeiconsIcon icon={Delete02Icon} />
          Delete collection
        </Button>
      </div>

      <div
        onDragOver={(e) => e.preventDefault()}
        onDrop={(e) => {
          e.preventDefault()
          const files = [...e.dataTransfer.files]
          if (files.length > 0) void uploadFiles(files)
        }}
        className="flex flex-col items-center gap-2 rounded-xl border border-dashed border-border p-6 text-center"
      >
        <input
          ref={inputRef}
          type="file"
          accept={acceptExt}
          multiple
          className="hidden"
          onChange={(e) => {
            const files = [...(e.target.files ?? [])]
            e.target.value = ''
            if (files.length > 0) void uploadFiles(files)
          }}
        />
        <HugeiconsIcon icon={CloudUploadIcon} className="size-6 text-muted-foreground" />
        <p className="text-sm text-muted-foreground">
          Drag files here or{' '}
          <button type="button" onClick={() => inputRef.current?.click()} className="text-brand underline">
            browse
          </button>
          {' '}· {acceptExt}
        </p>
        {Object.keys(uploading).length > 0 && (
          <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
            <HugeiconsIcon icon={Loading03Icon} className="size-3 animate-spin" />
            Uploading {Object.keys(uploading).length} file
            {Object.keys(uploading).length === 1 ? '' : 's'}…
          </p>
        )}
      </div>

      <form
        onSubmit={(e) => {
          e.preventDefault()
          void addUrl()
        }}
        className="flex items-center gap-2"
      >
        <input
          type="url"
          value={url}
          onChange={(e) => setUrl(e.target.value)}
          placeholder="https://example.com/article — add a page or PDF by URL"
          className="h-9 w-full rounded-md border border-border bg-transparent px-3 text-sm outline-none placeholder:text-muted-foreground focus:border-ring"
        />
        <Button type="submit" variant="outline" disabled={!url.trim() || addingUrl}>
          {addingUrl ? (
            <HugeiconsIcon icon={Loading03Icon} className="animate-spin" />
          ) : (
            'Add URL'
          )}
        </Button>
      </form>

      {documents.length === 0 ? (
        <p className="text-sm text-muted-foreground">No documents yet.</p>
      ) : (
        <div className="overflow-x-auto rounded-xl border border-border">
          <table className="w-full text-sm">
            <thead className="border-b border-border bg-muted/50 text-left text-xs uppercase tracking-wide text-muted-foreground">
              <tr>
                <th className="px-3 py-2">Title</th>
                <th className="px-3 py-2">Source</th>
                <th className="px-3 py-2">Status</th>
                <th className="px-3 py-2">Chunks</th>
                <th className="px-3 py-2">Size</th>
                <th className="px-3 py-2">Ingested</th>
                <th className="px-3 py-2" />
              </tr>
            </thead>
            <tbody>
              {documents.map((doc) => (
                <tr key={doc.id} className="border-b border-border last:border-0">
                  <td className="flex items-center gap-2 px-3 py-2">
                    <HugeiconsIcon icon={File02Icon} className="size-4 shrink-0 text-muted-foreground" />
                    <span className="truncate">{doc.title}</span>
                  </td>
                  <td className="px-3 py-2">
                    <span className="rounded bg-muted px-1.5 py-0.5 text-xs font-medium uppercase text-muted-foreground">
                      {doc.source_type}
                    </span>
                  </td>
                  <td className="px-3 py-2">
                    <StatusBadge doc={doc} />
                  </td>
                  <td className="px-3 py-2">{doc.chunk_count}</td>
                  <td className="px-3 py-2">{humanBytes(doc.bytes)}</td>
                  <td className="px-3 py-2">{doc.ingested_at ? relativeTime(doc.ingested_at) : '—'}</td>
                  <td className="px-3 py-2">
                    <div className="flex items-center justify-end gap-2">
                      <button
                        type="button"
                        aria-label={`Re-ingest ${doc.title}`}
                        onClick={() => void reingest(doc)}
                        className="text-muted-foreground hover:text-foreground"
                      >
                        <HugeiconsIcon icon={ReloadIcon} className="size-4" />
                      </button>
                      <button
                        type="button"
                        aria-label={`Delete ${doc.title}`}
                        onClick={() => setConfirmDeleteDoc(doc)}
                        className="text-muted-foreground hover:text-destructive"
                      >
                        <HugeiconsIcon icon={Delete02Icon} className="size-4" />
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <Dialog open={confirmDeleteCollection} onOpenChange={setConfirmDeleteCollection}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete {collection.name}?</DialogTitle>
          </DialogHeader>
          <p className="text-sm text-muted-foreground">
            Removes the collection and every document in it. Agents that searched it lose access
            immediately.
          </p>
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfirmDeleteCollection(false)}>
              Cancel
            </Button>
            <Button variant="destructive" onClick={() => void removeCollection()}>
              Delete
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={confirmDeleteDoc !== null} onOpenChange={(o) => !o && setConfirmDeleteDoc(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete {confirmDeleteDoc?.title}?</DialogTitle>
          </DialogHeader>
          <p className="text-sm text-muted-foreground">
            Removes the document and its indexed chunks from the collection.
          </p>
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfirmDeleteDoc(null)}>
              Cancel
            </Button>
            <Button variant="destructive" onClick={() => void removeDoc()}>
              Delete
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
