import { useEffect, useState } from 'react'
import { memoryChain } from '../../api/client'
import type { MemoryItem } from '../../api/types'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from '../ui/dialog'
import { TypeBadge } from './TypeBadge'

// ChainDialog shows a memory's supersede history, oldest first.
export function ChainDialog({ id, onClose }: { id: string; onClose: () => void }) {
  const [chain, setChain] = useState<MemoryItem[]>([])
  useEffect(() => {
    memoryChain(id).then(setChain).catch(() => setChain([]))
  }, [id])
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Supersede history</DialogTitle>
        </DialogHeader>
        <ol className="space-y-2" data-testid="chain-list">
          {chain.map((m, i) => (
            <li key={m.id} className="rounded border p-3 text-sm">
              <div className="mb-1 flex items-center gap-2">
                <span className="text-xs text-muted-foreground">v{i + 1}</span>
                <TypeBadge type={m.type} />
                <span className="text-xs text-muted-foreground">{m.status}</span>
              </div>
              {m.content}
            </li>
          ))}
        </ol>
      </DialogContent>
    </Dialog>
  )
}
