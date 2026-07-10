import { useCallback, useEffect, useRef, useState } from 'react'
import { listSessions } from '../api/client'
import type { SessionMeta } from '../api/types'
import { SessionsContext } from '../lib/sessions'

export function SessionsProvider({ children }: { children: React.ReactNode }) {
  const [sessions, setSessions] = useState<SessionMeta[]>([])
  const [query, setQuery] = useState('')
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)

  const load = useCallback((q: string) => {
    listSessions(q)
      .then(setSessions)
      .catch(() => {
        // No token yet or brain unreachable: an empty sidebar is the
        // honest render; the chat page surfaces the actual error.
      })
  }, [])

  const refresh = useCallback(() => {
    load(query)
    // The auto-title lands asynchronously after the first exchange:
    // follow up once so the sidebar picks it up without polling.
    if (timer.current) clearTimeout(timer.current)
    timer.current = setTimeout(() => load(query), 5000)
  }, [load, query])

  useEffect(() => {
    load(query)
    return () => {
      if (timer.current) clearTimeout(timer.current)
    }
  }, [load, query])

  return (
    <SessionsContext.Provider value={{ sessions, query, setQuery, refresh }}>
      {children}
    </SessionsContext.Provider>
  )
}
