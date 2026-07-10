import { createContext, useContext } from 'react'
import type { SessionMeta } from '../api/types'

// SessionsContext holds the sidebar's session directory: the paged
// list, the active search query, and a refresh shared with the chat
// page (a finished turn changes updated_at and, on the first exchange,
// the auto-title — the sidebar must follow).
export interface SessionsState {
  sessions: SessionMeta[]
  query: string
  setQuery: (q: string) => void
  refresh: () => void
  hasMore: boolean
  loadMore: () => void
}

export const SessionsContext = createContext<SessionsState>({
  sessions: [],
  query: '',
  setQuery: () => {},
  refresh: () => {},
  hasMore: false,
  loadMore: () => {},
})

export function useSessions() {
  return useContext(SessionsContext)
}

// groupByDay buckets sessions under human day labels, newest first.
// The list arrives ordered by updated_at from the API.
export function groupByDay(
  sessions: SessionMeta[],
  now = new Date(),
): { label: string; sessions: SessionMeta[] }[] {
  const dayKey = (d: Date) => d.toDateString()
  const today = dayKey(now)
  const yesterday = dayKey(new Date(now.getTime() - 24 * 60 * 60 * 1000))

  const groups: { label: string; sessions: SessionMeta[] }[] = []
  for (const s of sessions) {
    const d = new Date(s.updated_at)
    const key = dayKey(d)
    const label =
      key === today
        ? 'Today'
        : key === yesterday
          ? 'Yesterday'
          : d.toLocaleDateString('en-US', { month: 'long', day: 'numeric' })
    const last = groups[groups.length - 1]
    if (last && last.label === label) last.sessions.push(s)
    else groups.push({ label, sessions: [s] })
  }
  return groups
}
