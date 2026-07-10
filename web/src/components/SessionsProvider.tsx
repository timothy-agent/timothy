import { useCallback, useEffect, useRef, useState } from 'react'
import { listSessions } from '../api/client'
import type { SessionMeta } from '../api/types'
import { SessionsContext } from '../lib/sessions'

// Mirrors the server's page size: a full page means another may exist.
const pageSize = 100

export function SessionsProvider({ children }: { children: React.ReactNode }) {
  const [sessions, setSessions] = useState<SessionMeta[]>([])
  const [query, setQuery] = useState('')
  const [hasMore, setHasMore] = useState(false)
  const titleTimer = useRef<ReturnType<typeof setTimeout> | null>(null)

  const load = useCallback((q: string, before = '') => {
    listSessions(q, before)
      .then((page) => {
        setSessions((prev) => (before ? [...prev, ...page] : page))
        setHasMore(page.length === pageSize)
      })
      .catch(() => {
        // No token yet or brain unreachable: an empty sidebar is the
        // honest render; the chat page surfaces the actual error.
      })
  }, [])

  const refresh = useCallback(() => {
    load(query)
    // The auto-title lands asynchronously after the first exchange:
    // follow up once so the sidebar picks it up without polling.
    if (titleTimer.current) clearTimeout(titleTimer.current)
    titleTimer.current = setTimeout(() => load(query), 5000)
  }, [load, query])

  const loadMore = useCallback(() => {
    setSessions((prev) => {
      const last = prev[prev.length - 1]
      if (last) load(query, last.updated_at)
      return prev
    })
  }, [load, query])

  // Debounced: typing in the search box fires one request per pause,
  // not one per keystroke.
  useEffect(() => {
    const t = setTimeout(() => load(query), 250)
    return () => clearTimeout(t)
  }, [load, query])

  useEffect(
    () => () => {
      if (titleTimer.current) clearTimeout(titleTimer.current)
    },
    [],
  )

  return (
    <SessionsContext.Provider value={{ sessions, query, setQuery, refresh, hasMore, loadMore }}>
      {children}
    </SessionsContext.Provider>
  )
}
