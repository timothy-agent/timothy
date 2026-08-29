import { Add01Icon, CloudUploadIcon, LibraryIcon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useCallback, useEffect, useState } from 'react'
import { useNavigate } from 'react-router'
import { toast } from 'sonner'
import { listKbCollections } from '../../api/client'
import type { KbCollection } from '../../api/types'
import { relativeTime } from '../../lib/format'
import { Button } from '../ui/button'
import { errText } from '../settings/util'

export function KnowledgeCollectionsList() {
  const [collections, setCollections] = useState<KbCollection[]>([])
  const navigate = useNavigate()

  const refresh = useCallback(() => {
    listKbCollections()
      .then(setCollections)
      .catch((err: unknown) => toast.error('Could not load collections', { description: errText(err) }))
  }, [])
  useEffect(refresh, [refresh])

  return (
    <div className="mt-6 space-y-6">
      <p className="text-sm text-muted-foreground">
        Collections group documents an agent can search with search_kb for grounded answers.
        Upload files to a collection, then allow an agent to search it from the agent's own
        settings.
      </p>

      <div className="flex items-center justify-between">
        <h2 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
          Collections · {collections.length}
        </h2>
        <div className="flex items-center gap-2">
          <Button variant="outline" onClick={() => navigate('/knowledge/add')}>
            <HugeiconsIcon icon={CloudUploadIcon} />
            Add to Knowledgebase
          </Button>
          <Button onClick={() => navigate('/knowledge/new')}>
            <HugeiconsIcon icon={Add01Icon} />
            New collection
          </Button>
        </div>
      </div>

      {collections.length === 0 ? (
        <div className="flex flex-col items-center gap-3 rounded-xl border border-dashed border-border p-10 text-center">
          <span className="flex size-10 items-center justify-center rounded-lg bg-brand-soft text-brand-soft-foreground">
            <HugeiconsIcon icon={LibraryIcon} className="size-5" />
          </span>
          <p className="text-sm text-muted-foreground">
            No collections yet. Create one and upload documents so agents can search them for
            grounded answers.
          </p>
          <Button onClick={() => navigate('/knowledge/new')}>
            <HugeiconsIcon icon={Add01Icon} />
            New collection
          </Button>
        </div>
      ) : (
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {collections.map((c) => (
            <button
              key={c.id}
              type="button"
              onClick={() => navigate(`/knowledge/${c.id}`)}
              className="flex flex-col gap-3 rounded-xl border border-border bg-card p-4 text-left shadow-sm transition hover:shadow-md"
            >
              <div className="flex items-center gap-3">
                <span className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-brand-soft text-brand-soft-foreground">
                  <HugeiconsIcon icon={LibraryIcon} className="size-4.5" />
                </span>
                <span className="min-w-0 flex-1 truncate text-sm font-semibold">{c.name}</span>
              </div>
              {c.description && (
                <p className="line-clamp-2 text-sm text-muted-foreground">{c.description}</p>
              )}
              <p className="mt-auto text-xs text-muted-foreground">
                {c.doc_count} doc{c.doc_count === 1 ? '' : 's'} · {c.chunk_count} chunk
                {c.chunk_count === 1 ? '' : 's'} · updated {relativeTime(c.updated_at)}
                {c.failed_count > 0 && (
                  <span className="text-red-600 dark:text-red-400">
                    {' '}
                    · {c.failed_count} failed
                  </span>
                )}
              </p>
            </button>
          ))}
        </div>
      )}
    </div>
  )
}
