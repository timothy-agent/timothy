import { useEffect, useState } from 'react'
import { toast } from 'sonner'
import { entityMemories } from '../../api/client'
import type { EntityNode, MemoryItem } from '../../api/types'
import { ChainDialog } from './ChainDialog'
import { TypeBadge } from './TypeBadge'

// Confidence thresholds map to the semantic tokens: solid ≥ 0.7,
// shaky in between, doubtful below 0.4.
function confidenceClass(c: number): string {
  if (c >= 0.7) return 'bg-good'
  if (c >= 0.4) return 'bg-warning'
  return 'bg-destructive'
}

// EntityDetailPanel lists the active memories behind one graph node.
export function EntityDetailPanel({ entity }: { entity: EntityNode }) {
  const [memories, setMemories] = useState<MemoryItem[] | null>(null)
  const [chainFor, setChainFor] = useState<string | null>(null)

  useEffect(() => {
    setMemories(null)
    entityMemories(entity.id)
      .then(setMemories)
      .catch(() => {
        toast.error('Could not load entity memories')
        setMemories([])
      })
  }, [entity.id])

  return (
    <div className="space-y-3" data-testid="entity-detail">
      <div className="flex items-center gap-2">
        <h3 className="min-w-0 flex-1 truncate text-sm font-semibold">{entity.name}</h3>
        <span className="rounded bg-muted px-1.5 py-0.5 text-xs text-muted-foreground">
          {entity.type}
        </span>
      </div>
      <p className="text-xs text-muted-foreground">
        {entity.memory_count} active {entity.memory_count === 1 ? 'memory' : 'memories'}
      </p>
      {memories === null ? (
        <p className="text-sm text-muted-foreground">Loading…</p>
      ) : memories.length === 0 ? (
        <p className="text-sm text-muted-foreground">No active memories reference this entity.</p>
      ) : (
        <div className="space-y-2">
          {memories.map((m) => (
            <div key={m.id} className="space-y-2 rounded-lg border p-3 text-sm" data-testid="entity-memory">
              <div className="flex items-center gap-2">
                <TypeBadge type={m.type} />
                <span className="text-xs text-muted-foreground">
                  {new Date(m.created_at).toLocaleDateString()}
                </span>
                <button
                  type="button"
                  className="ml-auto text-xs text-muted-foreground underline-offset-2 hover:underline"
                  onClick={() => setChainFor(m.id)}
                >
                  history
                </button>
              </div>
              <p className="leading-relaxed">{m.content}</p>
              <div
                className="h-1 overflow-hidden rounded-full bg-muted"
                title={`confidence ${Math.round(m.confidence * 100)}%`}
              >
                <div
                  className={`h-full rounded-full ${confidenceClass(m.confidence)}`}
                  style={{ width: `${Math.round(m.confidence * 100)}%` }}
                />
              </div>
            </div>
          ))}
        </div>
      )}
      {chainFor && <ChainDialog id={chainFor} onClose={() => setChainFor(null)} />}
    </div>
  )
}
