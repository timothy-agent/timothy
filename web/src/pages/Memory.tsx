import { useCallback, useEffect, useState } from 'react'
import { Link, Navigate, useParams } from 'react-router'
import { toast } from 'sonner'
import {
  addMemory,
  listMemories,
  resolveMemory,
  searchMemories,
} from '../api/client'
import type { MemoryItem, RetrievedMemory } from '../api/types'
import { ChainDialog } from '../components/memory/ChainDialog'
import { GraphTab } from '../components/memory/GraphTab'
import { TypeBadge } from '../components/memory/TypeBadge'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '../components/ui/select'
import { Textarea } from '../components/ui/textarea'
import { notifyMemoryChanged } from '../lib/memory'

// QueueCard is one pending memory awaiting the user's verdict.
function QueueCard({
  memory,
  onResolved,
}: {
  memory: MemoryItem
  onResolved: () => void
}) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(memory.content)
  const [busy, setBusy] = useState(false)

  const act = async (action: 'confirm' | 'reject', content?: string) => {
    setBusy(true)
    try {
      await resolveMemory(memory.id, action, content)
      notifyMemoryChanged()
      onResolved()
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="rounded-lg border p-4 space-y-3" data-testid="queue-card">
      <div className="flex items-center gap-2">
        <TypeBadge type={memory.type} />
        <span className="text-xs text-muted-foreground">
          confidence {Math.round(memory.confidence * 100)}%
        </span>
        {memory.source_session && (
          <Link
            to={`/sessions/${memory.source_session}`}
            className="text-xs text-muted-foreground underline-offset-2 hover:underline"
          >
            source session
          </Link>
        )}
      </div>
      {editing ? (
        <Textarea
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          rows={3}
          data-testid="edit-content"
        />
      ) : (
        <p className="text-sm leading-relaxed">{memory.content}</p>
      )}
      <div className="flex gap-2">
        {editing ? (
          <>
            <Button size="sm" disabled={busy || !draft.trim()} onClick={() => act('confirm', draft.trim())}>
              Save & confirm
            </Button>
            <Button size="sm" variant="ghost" disabled={busy} onClick={() => setEditing(false)}>
              Cancel
            </Button>
          </>
        ) : (
          <>
            <Button size="sm" disabled={busy} onClick={() => act('confirm')}>
              Confirm
            </Button>
            <Button size="sm" variant="outline" disabled={busy} onClick={() => setEditing(true)}>
              Edit
            </Button>
            <Button size="sm" variant="destructive" disabled={busy} onClick={() => act('reject')}>
              Reject
            </Button>
          </>
        )}
      </div>
    </div>
  )
}

function Queue() {
  const [pending, setPending] = useState<MemoryItem[]>([])
  const [loaded, setLoaded] = useState(false)
  const [bulkBusy, setBulkBusy] = useState(false)

  const refresh = useCallback(() => {
    listMemories('pending')
      .then(setPending)
      .catch(() => {
        toast.error('Could not load the memory queue')
        setPending([])
      })
      .finally(() => setLoaded(true))
  }, [])
  useEffect(refresh, [refresh])

  const bulk = async (action: 'confirm' | 'reject') => {
    setBulkBusy(true)
    try {
      await Promise.allSettled(pending.map((m) => resolveMemory(m.id, action)))
      notifyMemoryChanged()
      refresh()
    } finally {
      setBulkBusy(false)
    }
  }

  if (!loaded) return <p className="text-sm text-muted-foreground">Loading…</p>
  if (pending.length === 0)
    return <p className="text-sm text-muted-foreground">Queue is empty, nothing awaits confirmation.</p>

  return (
    <div className="space-y-3">
      <div className="flex items-center gap-2">
        <span className="text-sm text-muted-foreground">{pending.length} pending</span>
        <Button size="sm" variant="outline" disabled={bulkBusy} onClick={() => bulk('confirm')}>
          Confirm all
        </Button>
        <Button size="sm" variant="outline" disabled={bulkBusy} onClick={() => bulk('reject')}>
          Reject all
        </Button>
      </div>
      {pending.map((m) => (
        <QueueCard key={m.id} memory={m} onResolved={refresh} />
      ))}
    </div>
  )
}

function Browser() {
  const [query, setQuery] = useState('')
  const [results, setResults] = useState<RetrievedMemory[] | null>(null)
  const [browse, setBrowse] = useState<MemoryItem[]>([])
  const [status, setStatus] = useState<MemoryItem['status']>('active')
  const [chainFor, setChainFor] = useState<string | null>(null)
  const [newFact, setNewFact] = useState('')
  const [newType, setNewType] = useState('semantic')
  const [busy, setBusy] = useState(false)

  const loadBrowse = useCallback(() => {
    listMemories(status)
      .then(setBrowse)
      .catch(() => {
        toast.error('Could not load memories')
        setBrowse([])
      })
  }, [status])
  useEffect(loadBrowse, [loadBrowse])

  const search = async () => {
    if (!query.trim()) {
      setResults(null)
      return
    }
    setBusy(true)
    try {
      setResults(await searchMemories(query.trim()))
    } catch {
      toast.error('Search failed')
      setResults([])
    } finally {
      setBusy(false)
    }
  }

  const add = async () => {
    if (!newFact.trim()) return
    setBusy(true)
    try {
      await addMemory(newFact.trim(), newType)
      setNewFact('')
      notifyMemoryChanged()
      if (status === 'active') loadBrowse()
    } catch {
      toast.error('Could not save the memory')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center gap-2">
        <Input
          placeholder="Search memories…"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={(e) => e.key === 'Enter' && search()}
          data-testid="memory-search"
          className="h-10"
        />
        <Button onClick={search} disabled={busy}>
          Search
        </Button>
      </div>

      {results !== null && (
        <div className="space-y-2" data-testid="search-results">
          <h3 className="text-sm font-medium text-muted-foreground">
            {results.length === 0 ? 'Nothing retrieved.' : 'Retrieved (best first / runner-up last)'}
          </h3>
          {results.map((m) => (
            <div key={m.id} className="rounded border p-3 text-sm flex items-start gap-2">
              <TypeBadge type={m.type} />
              <span className="flex-1">{m.content}</span>
              <button
                type="button"
                className="text-xs text-muted-foreground underline-offset-2 hover:underline"
                onClick={() => setChainFor(m.id)}
              >
                history
              </button>
            </div>
          ))}
        </div>
      )}

      <div className="space-y-2">
        <div className="flex items-center gap-2">
          <h3 className="text-sm font-medium text-muted-foreground">Browse</h3>
          <Select value={status} onValueChange={(v) => setStatus(v as MemoryItem['status'])}>
            <SelectTrigger className="h-8 w-32" data-testid="status-filter">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="active">active</SelectItem>
              <SelectItem value="pending">pending</SelectItem>
              <SelectItem value="rejected">rejected</SelectItem>
              <SelectItem value="archived">archived</SelectItem>
            </SelectContent>
          </Select>
        </div>
        {browse.length === 0 ? (
          <p className="text-sm text-muted-foreground">No {status} memories.</p>
        ) : (
          browse.map((m) => (
            <div key={m.id} className="rounded border p-3 text-sm flex items-start gap-2">
              <TypeBadge type={m.type} />
              <span className="flex-1">{m.content}</span>
              <button
                type="button"
                className="text-xs text-muted-foreground underline-offset-2 hover:underline"
                onClick={() => setChainFor(m.id)}
              >
                history
              </button>
            </div>
          ))
        )}
      </div>

      <div className="space-y-2">
        <h3 className="text-sm font-medium text-muted-foreground">Remember something</h3>
        <div className="flex items-center gap-2">
          <Input
            placeholder="Timothy, remember…"
            value={newFact}
            onChange={(e) => setNewFact(e.target.value)}
            onKeyDown={(e) => e.key === 'Enter' && add()}
            data-testid="manual-add"
            className="h-10"
          />
          <Select value={newType} onValueChange={setNewType}>
            <SelectTrigger className="h-10 w-36">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="semantic">semantic</SelectItem>
              <SelectItem value="episodic">episodic</SelectItem>
              <SelectItem value="procedural">procedural</SelectItem>
            </SelectContent>
          </Select>
          <Button onClick={add} disabled={busy || !newFact.trim()}>
            Save
          </Button>
        </div>
        <p className="text-xs text-muted-foreground">
          Saved directly as an active memory, user-explicit facts skip the queue.
        </p>
      </div>

      {chainFor && <ChainDialog id={chainFor} onClose={() => setChainFor(null)} />}
    </div>
  )
}

const tabs = [
  { id: 'queue', label: 'Queue' },
  { id: 'browser', label: 'Browser' },
  { id: 'graph', label: 'Graph' },
] as const

// KnowledgeRedirect keeps old /memory/knowledge(/...) links working
// after the area moved to its own top-level page.
export function KnowledgeRedirect() {
  const { '*': rest } = useParams()
  return <Navigate to={`/knowledge${rest ? `/${rest}` : ''}`} replace />
}

export function Memory() {
  const [tab, setTab] = useState<(typeof tabs)[number]['id']>('queue')

  return (
    <div className="h-full overflow-y-auto">
      <div className="mx-auto w-full max-w-full space-y-6 p-6">
        <div className="flex items-center gap-2">
          <h1 className="text-lg font-semibold">Memory</h1>
          <div className="ml-auto flex gap-1 rounded-lg border p-0.5">
            {tabs.map((t) => (
              <Button
                key={t.id}
                size="sm"
                variant={tab === t.id ? 'secondary' : 'ghost'}
                onClick={() => setTab(t.id)}
                data-testid={`tab-${t.id}`}
              >
                {t.label}
              </Button>
            ))}
          </div>
        </div>
        {tab === 'queue' ? <Queue /> : tab === 'browser' ? <Browser /> : <GraphTab />}
      </div>
    </div>
  )
}
