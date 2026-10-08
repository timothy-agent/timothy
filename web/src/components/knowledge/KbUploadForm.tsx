import { CloudUpload, LoaderCircle } from 'lucide-react'
import { useRef, useState } from 'react'
import { toast } from 'sonner'
import type { KbDocument } from '../../api/types'
import { Button } from '../ui/button'
import { Input } from '../ui/input'
import { Textarea } from '../ui/textarea'
import { errText } from '../../lib/errors'
import { useOnboarding } from '../../onboarding/context'
import { unmetKey } from '../../onboarding/gateCopy'
import { SetupGate } from '../../onboarding/SetupGate'

const acceptExt = '.pdf,.md,.txt,.docx,.html'

// parseUrls splits pasted/typed text on whitespace into unique, valid
// http(s) URLs, preserving first-seen order.
export function parseUrls(text: string): string[] {
  const seen = new Set<string>()
  const urls: string[] = []
  for (const token of text.split(/\s+/)) {
    if (!token || seen.has(token)) continue
    try {
      const parsed = new URL(token)
      if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') continue
    } catch {
      continue
    }
    seen.add(token)
    urls.push(token)
  }
  return urls
}

// markdownFile wraps pasted markdown as a .md upload, which brain
// ingests without markitdown conversion. Path separators become '-'
// because the server keeps only a filename's last path segment.
export function markdownFile(title: string, markdown: string): File {
  const name = title.trim().replace(/[/\\]/g, '-')
  return new File([markdown], `${name}.md`, { type: 'text/markdown' })
}

// KbUploadForm is the drag/drop-file + paste-URL(s) + paste-markdown upload UI shared by
// a collection's own detail page (uploadFile/addUrl scoped to that
// collection) and the top-level auto-classify entry point (scoped to
// nothing — brain picks or creates the collection).
export function KbUploadForm({
  uploadFile,
  addUrl,
  onUploaded,
}: {
  uploadFile: (file: File) => Promise<KbDocument>
  addUrl: (url: string) => Promise<KbDocument>
  onUploaded: (doc: KbDocument) => void
}) {
  const [uploading, setUploading] = useState<Record<string, boolean>>({})
  const [url, setUrl] = useState('')
  const [urlProgress, setUrlProgress] = useState<{ done: number; total: number } | null>(null)
  const [mdTitle, setMdTitle] = useState('')
  const [markdown, setMarkdown] = useState('')
  const [addingMarkdown, setAddingMarkdown] = useState(false)
  const inputRef = useRef<HTMLInputElement>(null)
  const { readiness } = useOnboarding()
  const blocked = unmetKey(readiness, ['embedding_route']) !== null

  const uploadFiles = async (files: File[]) => {
    if (blocked) return
    for (const file of files) {
      const key = crypto.randomUUID()
      setUploading((prev) => ({ ...prev, [key]: true }))
      try {
        const doc = await uploadFile(file)
        onUploaded(doc)
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

  const submitUrls = async () => {
    if (urlProgress) return
    const urls = parseUrls(url)
    if (urls.length === 0) return
    setUrlProgress({ done: 0, total: urls.length })
    const failed: string[] = []
    for (const [i, u] of urls.entries()) {
      try {
        const doc = await addUrl(u)
        onUploaded(doc)
      } catch {
        failed.push(u)
      }
      setUrlProgress({ done: i + 1, total: urls.length })
    }
    setUrlProgress(null)
    setUrl('')
    if (failed.length > 0) {
      toast.error(`${failed.length} of ${urls.length} failed: ${failed[0]}${failed.length > 1 ? '…' : ''}`)
    }
  }

  const canAddMarkdown = mdTitle.trim() !== '' && markdown.trim() !== '' && !addingMarkdown

  const submitMarkdown = async () => {
    if (!canAddMarkdown) return
    setAddingMarkdown(true)
    try {
      const doc = await uploadFile(markdownFile(mdTitle, markdown))
      onUploaded(doc)
      setMdTitle('')
      setMarkdown('')
    } catch (err) {
      toast.error(`${mdTitle.trim()}: upload failed`, { description: errText(err) })
    } finally {
      setAddingMarkdown(false)
    }
  }

  return (
    <div className="space-y-2">
      {blocked && <SetupGate requires={['embedding_route']} variant="banner" />}
      <fieldset disabled={blocked} className="min-w-0 space-y-2 disabled:opacity-60">
        <div
          onDragOver={(e) => e.preventDefault()}
          onDrop={(e) => {
            e.preventDefault()
            const files = [...e.dataTransfer.files]
            if (files.length > 0) void uploadFiles(files)
          }}
          className="flex flex-col items-center gap-2 rounded-md border border-dashed border-border p-6 text-center"
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
          <CloudUpload className="size-6 text-muted-foreground" />
          <p className="text-sm text-muted-foreground">
            Drag files here or{' '}
            <button type="button" onClick={() => inputRef.current?.click()} className="text-brand underline">
              browse
            </button>
            {' '}· {acceptExt}
          </p>
          {Object.keys(uploading).length > 0 && (
            <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
              <LoaderCircle className="size-3 animate-spin" />
              Uploading {Object.keys(uploading).length} file
              {Object.keys(uploading).length === 1 ? '' : 's'}…
            </p>
          )}
        </div>

        <form
          onSubmit={(e) => {
            e.preventDefault()
            void submitUrls()
          }}
          className="flex items-end gap-2"
        >
          <textarea
            rows={1}
            value={url}
            onChange={(e) => {
              setUrl(e.target.value)
              e.target.style.height = 'auto'
              e.target.style.height = `${e.target.scrollHeight}px`
            }}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && !e.shiftKey) {
                e.preventDefault()
                void submitUrls()
              }
            }}
            placeholder="https://example.com/article — add a page or PDF by URL (paste several to bulk-add)"
            className="max-h-40 min-h-9 w-full resize-none rounded-md border border-border bg-transparent px-3 py-2 text-sm outline-none placeholder:text-muted-foreground focus:border-ring"
          />
          <Button type="submit" variant="outline" disabled={parseUrls(url).length === 0 || !!urlProgress}>
            {urlProgress ? (
              <>
                <LoaderCircle className="animate-spin" />
                adding {urlProgress.done}/{urlProgress.total}…
              </>
            ) : (
              'Add URL'
            )}
          </Button>
        </form>

        <form
          onSubmit={(e) => {
            e.preventDefault()
            void submitMarkdown()
          }}
          className="space-y-2"
        >
          <Input value={mdTitle} onChange={(e) => setMdTitle(e.target.value)} placeholder="Markdown title" />
          <Textarea
            rows={4}
            value={markdown}
            onChange={(e) => setMarkdown(e.target.value)}
            placeholder="Paste or write markdown"
            className="max-h-80 font-mono"
          />
          <div className="flex justify-end">
            <Button type="submit" variant="outline" disabled={!canAddMarkdown}>
              {addingMarkdown ? (
                <>
                  <LoaderCircle className="animate-spin" />
                  adding…
                </>
              ) : (
                'Add markdown'
              )}
            </Button>
          </div>
        </form>
      </fieldset>
    </div>
  )
}
