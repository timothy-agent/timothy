import { useCallback, useEffect, useState } from 'react'
import { toast } from 'sonner'
import { entityGraph } from '../../api/client'
import type { EntityGraphData } from '../../api/types'
import { memoryChangedEvent } from '../../lib/memory'
import { EntityDetailPanel } from './EntityDetailPanel'
import { EntityGraph } from './EntityGraph'

// GraphTab is the Memory page's knowledge-graph view: the entity map
// on the left, the selected entity's memories on the right. Refreshes
// whenever memories change elsewhere (confirm/reject/add).
export function GraphTab() {
  const [data, setData] = useState<EntityGraphData>({ entities: [], edges: [] })
  const [loaded, setLoaded] = useState(false)
  const [selectedId, setSelectedId] = useState<string | null>(null)

  const refresh = useCallback(() => {
    entityGraph()
      .then(setData)
      .catch(() => {
        toast.error('Could not load entity graph')
        setData({ entities: [], edges: [] })
      })
      .finally(() => setLoaded(true))
  }, [])
  useEffect(() => {
    refresh()
    window.addEventListener(memoryChangedEvent, refresh)
    return () => window.removeEventListener(memoryChangedEvent, refresh)
  }, [refresh])

  if (!loaded) return <p className="text-sm text-muted-foreground">Loading…</p>

  const selected = data.entities.find((e) => e.id === selectedId) ?? null

  return (
    <div className="flex flex-col gap-6 lg:flex-row">
      <div className="min-w-0 flex-1">
        <EntityGraph data={data} selectedId={selectedId} onSelect={setSelectedId} />
      </div>
      {selected ? (
        <div className="lg:w-80 lg:shrink-0">
          <EntityDetailPanel entity={selected} />
        </div>
      ) : (
        data.entities.length > 0 && (
          <p className="text-sm text-muted-foreground lg:w-80 lg:shrink-0">
            Select an entity to see the memories behind it.
          </p>
        )
      )}
    </div>
  )
}
