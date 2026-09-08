import { Add01Icon, CloudUploadIcon, LibraryIcon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { Library } from 'lucide-react'
import { useCallback, useEffect, useState } from 'react'
import { useNavigate } from 'react-router'
import { toast } from 'sonner'
import { listKbCollections } from '../../api/client'
import type { KbCollection } from '../../api/types'
import { relativeTime } from '../../lib/format'
import { Button } from '../ui/button'
import { Card } from '../ui/card'
import { errText } from '../../lib/errors'
import { EmptyState } from '../timothy/empty-state'
import { Eyebrow, PageHeader } from '../timothy/page-header'

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
    <>
      <PageHeader
        title="Knowledge"
        description="Collections group documents an agent can search with search_kb for grounded answers. Upload files to a collection, then allow an agent to search it from the agent's own settings."
        actions={
          <>
            <Button variant="outline" onClick={() => navigate('/knowledge/add')}>
              <HugeiconsIcon icon={CloudUploadIcon} />
              Add to Knowledgebase
            </Button>
            <Button onClick={() => navigate('/knowledge/new')}>
              <HugeiconsIcon icon={Add01Icon} />
              New collection
            </Button>
          </>
        }
      />

      <h2 className="mb-4">
        <Eyebrow>Collections · {collections.length}</Eyebrow>
      </h2>

      {collections.length === 0 ? (
        <div className="rounded-md border border-dashed border-border">
          <EmptyState
            icon={Library}
            title="No collections yet."
            description="Create one and upload documents so agents can search them for grounded answers."
            action={
              <Button onClick={() => navigate('/knowledge/new')}>
                <HugeiconsIcon icon={Add01Icon} />
                New collection
              </Button>
            }
          />
        </div>
      ) : (
        <div className="mt-5 grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {collections.map((c) => (
            <Card key={c.id} interactive asChild className="flex flex-col gap-3 text-left">
              <button type="button" onClick={() => navigate(`/knowledge/${c.id}`)} aria-label={c.name}>
                <div className="flex items-center gap-3">
                  <span className="flex size-9 shrink-0 items-center justify-center rounded-md bg-brand-soft text-brand-soft-foreground">
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
                  {c.failed_count > 0 && <span className="text-destructive"> · {c.failed_count} failed</span>}
                </p>
              </button>
            </Card>
          ))}
        </div>
      )}
    </>
  )
}
