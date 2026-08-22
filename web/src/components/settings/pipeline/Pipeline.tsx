import type { AdminProvider, ChainEntry, RouteEntryStatus } from '../../../api/types'
import { PipelineCard } from './PipelineCard'
import { reorder, useReorderDrag } from './useReorderDrag'

// One card of the pipeline: the raw chain entry plus the router's
// live status for it, when known.
export interface PipelineEntry {
  entry: ChainEntry
  status?: RouteEntryStatus
}

export function Pipeline({
  entries,
  scored,
  serving,
  providers,
  onReorder,
  onRemove,
}: {
  entries: PipelineEntry[]
  scored: boolean
  serving?: ChainEntry
  providers: AdminProvider[]
  // from/to are positions in the DISPLAYED order; the parent maps them
  // back to chain indices (identical for ordered routes).
  onReorder: (from: number, to: number) => void
  onRemove: (displayIndex: number) => void
}) {
  const { dragIndex, overIndex, handleProps, setItemRef } = useReorderDrag({
    disabled: scored,
    onCommit: onReorder,
  })

  // Live preview: show the list as it would land if dropped here.
  const display =
    dragIndex !== null && overIndex !== null && overIndex !== dragIndex
      ? reorder(entries, dragIndex, overIndex)
      : entries
  const previewing = display !== entries

  const maxScore = Math.max(0, ...entries.map((e) => e.status?.score ?? 0))
  const providerOf = (id: string) => providers.find((p) => p.id === id)

  if (entries.length === 0) {
    return <p className="text-sm text-muted-foreground">No providers in this chain yet.</p>
  }

  return (
    <div className="grid grid-cols-1 gap-2 sm:grid-cols-2 xl:grid-cols-4" data-testid="pipeline">
      {display.map((e, i) => {
        const original = entries.indexOf(e)
        const provider = providerOf(e.entry.provider_id)
        const isServing =
          !previewing &&
          serving !== undefined &&
          serving.provider_id === e.entry.provider_id &&
          serving.model === e.entry.model
        return (
          <div
            key={`${e.entry.provider_id}-${e.entry.model}-${original}`}
            ref={setItemRef(i)}
            {...handleProps(original)}
          >
            <PipelineCard
              provider={provider}
              name={e.status?.provider_name ?? provider?.name ?? e.entry.provider_id.slice(0, 8)}
              model={e.status?.model ?? e.entry.model}
              status={e.status}
              serving={isServing}
              scored={scored}
              maxScore={maxScore}
              index={original}
              count={entries.length}
              onMove={onReorder}
              onRemove={() => onRemove(original)}
              dragging={dragIndex === original}
            />
          </div>
        )
      })}
    </div>
  )
}
