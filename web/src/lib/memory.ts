import { useCallback, useEffect, useRef, useState } from 'react'
import { toast } from 'sonner'
import { countMemories, type SessionCursor } from '../api/client'
import type { MemoryItem } from '../api/types'

// memoryChangedEvent links the Memory page to the sidebar badge
// without a shared provider: queue actions dispatch, the badge
// listens.
export const memoryChangedEvent = 'timothy:memory-changed'

export function notifyMemoryChanged() {
  window.dispatchEvent(new Event(memoryChangedEvent))
}

// usePendingMemories keeps badges current: initial fetch, a slow
// poll, and an instant refresh when the Memory page acts. It reads the
// count endpoint, never the list.
export function usePendingMemories(): number {
  const [count, setCount] = useState(0)
  useEffect(() => {
    let live = true
    const refresh = () => {
      countMemories('pending')
        .then((n) => live && setCount(n))
        .catch(() => {})
    }
    refresh()
    const timer = setInterval(refresh, 60_000)
    window.addEventListener(memoryChangedEvent, refresh)
    return () => {
      live = false
      clearInterval(timer)
      window.removeEventListener(memoryChangedEvent, refresh)
    }
  }, [])
  return count
}

// Mirrors the server's default page size: a full page means another may exist.
export const memoryPageSize = 50

interface Pages {
  items: MemoryItem[]
  hasMore: boolean
  loaded: boolean
}

// sortsAfter is true when a comes after b in the server's
// created_at DESC, id DESC order.
function sortsAfter(a: MemoryItem, b: MemoryItem): boolean {
  const ta = Date.parse(a.created_at)
  const tb = Date.parse(b.created_at)
  return ta < tb || (ta === tb && a.id < b.id)
}

// mergeFirstPage folds a refetched first page into the loaded rows:
// page rows replace their stale copies, loaded rows older than the page
// stay, and rows the page should hold but lacks drop out.
function mergeFirstPage(prev: Pages, page: MemoryItem[]): Pages {
  if (page.length < memoryPageSize) return { items: page, hasMore: false, loaded: true }
  const ids = new Set(page.map((m) => m.id))
  const last = page[page.length - 1]
  const older = prev.items.filter((m) => !ids.has(m.id) && sortsAfter(m, last))
  return { items: [...page, ...older], hasMore: older.length > 0 ? prev.hasMore : true, loaded: true }
}

// useMemoryPages pages a newest-first memory list. load fetches page 1
// with no cursor, later pages with the previous last row; a new load
// (filter change) restarts at page 1. refresh refetches page 1 only and
// merges it by id; drop removes rows the user just resolved.
export function useMemoryPages(
  load: (cursor?: SessionCursor) => Promise<MemoryItem[]>,
  errorMessage: string,
) {
  const [pages, setPages] = useState<Pages>({ items: [], hasMore: false, loaded: false })
  // gen bumps on every new load so responses for an old filter drop.
  const gen = useRef(0)
  const loadingMore = useRef(false)
  const sentinelRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const g = ++gen.current
    loadingMore.current = false
    setPages({ items: [], hasMore: false, loaded: false })
    load().then(
      (page) => {
        if (g === gen.current) setPages({ items: page, hasMore: page.length === memoryPageSize, loaded: true })
      },
      () => {
        if (g !== gen.current) return
        toast.error(errorMessage)
        setPages({ items: [], hasMore: false, loaded: true })
      },
    )
  }, [load, errorMessage])

  const refresh = useCallback(() => {
    const g = gen.current
    load().then((page) => {
      if (g === gen.current) setPages((prev) => mergeFirstPage(prev, page))
    }, () => undefined)
  }, [load])

  const drop = useCallback((ids: string[]) => {
    const gone = new Set(ids)
    setPages((prev) => ({ ...prev, items: prev.items.filter((m) => !gone.has(m.id)) }))
  }, [])

  const items = pages.items
  const loadMore = useCallback(() => {
    const last = items[items.length - 1]
    if (!last || loadingMore.current) return
    loadingMore.current = true
    const g = gen.current
    load({ before: last.created_at, beforeId: last.id })
      .then((page) => {
        if (g !== gen.current) return
        setPages((prev) => {
          const seen = new Set(prev.items.map((m) => m.id))
          return {
            items: [...prev.items, ...page.filter((m) => !seen.has(m.id))],
            hasMore: page.length === memoryPageSize,
            loaded: true,
          }
        })
      }, () => undefined)
      .finally(() => {
        if (g === gen.current) loadingMore.current = false
      })
  }, [items, load])

  // Infinite scroll: fetch the next page when the sentinel below the
  // list scrolls into view.
  useEffect(() => {
    const el = sentinelRef.current
    if (!el || !pages.hasMore) return
    const obs = new IntersectionObserver((entries) => {
      if (entries.some((e) => e.isIntersecting)) loadMore()
    })
    obs.observe(el)
    return () => obs.disconnect()
  }, [pages.hasMore, loadMore])

  return { items, loaded: pages.loaded, hasMore: pages.hasMore, sentinelRef, refresh, drop }
}
