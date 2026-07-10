import { ArchiveBoxIcon, MagnifyingGlassIcon, PencilIcon, PlusIcon } from '@heroicons/react/20/solid'
import { useEffect, useRef, useState } from 'react'
import { useLocation, useNavigate } from 'react-router'
import { updateSession } from '../api/client'
import type { SessionMeta } from '../api/types'
import { groupByDay, useSessions } from '../lib/sessions'
import { Badge } from './catalyst/badge'
import { Button } from './catalyst/button'
import { Dialog, DialogActions, DialogBody, DialogTitle } from './catalyst/dialog'
import { Input } from './catalyst/input'
import { SidebarHeading, SidebarItem, SidebarLabel, SidebarSection } from './catalyst/sidebar'

export function SessionList() {
  const { sessions, query, setQuery, refresh, hasMore, loadMore } = useSessions()
  const { pathname } = useLocation()
  const navigate = useNavigate()
  const [renaming, setRenaming] = useState<SessionMeta | null>(null)
  const [title, setTitle] = useState('')
  const sentinelRef = useRef<HTMLDivElement>(null)

  // Infinite scroll: fetch the next page when the sentinel below the
  // list scrolls into view.
  useEffect(() => {
    const el = sentinelRef.current
    if (!el || !hasMore) return
    const obs = new IntersectionObserver((entries) => {
      if (entries.some((e) => e.isIntersecting)) loadMore()
    })
    obs.observe(el)
    return () => obs.disconnect()
  }, [hasMore, loadMore])

  const rename = async () => {
    if (!renaming || title.trim() === '') return
    await updateSession(renaming.id, { title: title.trim() }).catch(() => {})
    setRenaming(null)
    refresh()
  }

  const archive = async (s: SessionMeta) => {
    await updateSession(s.id, { archived: !s.archived }).catch(() => {})
    refresh()
    // Archiving the open session sends you back to a fresh chat.
    if (!s.archived && pathname === `/sessions/${s.id}`) navigate('/')
  }

  return (
    <>
      <SidebarSection>
        <SidebarItem href="/" current={pathname === '/'}>
          <PlusIcon data-slot="icon" />
          <SidebarLabel>New chat</SidebarLabel>
        </SidebarItem>
        <div className="relative mt-1 px-0.5">
          <MagnifyingGlassIcon className="pointer-events-none absolute top-2.5 left-2.5 size-4 text-zinc-400" />
          <input
            aria-label="Search sessions"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search…"
            className="w-full rounded-lg border border-zinc-950/10 bg-transparent py-1.5 pr-2 pl-8 text-sm text-zinc-900 outline-none placeholder:text-zinc-400 focus:border-blue-500/50 dark:border-white/10 dark:text-white"
          />
        </div>
      </SidebarSection>

      {groupByDay(sessions).map((group) => (
        <SidebarSection key={group.label}>
          <SidebarHeading>{group.label}</SidebarHeading>
          {group.sessions.map((s) => (
            <div key={s.id} className="group/session relative">
              <SidebarItem href={`/sessions/${s.id}`} current={pathname === `/sessions/${s.id}`}>
                <SidebarLabel className="truncate pr-10">{s.title || 'New session'}</SidebarLabel>
                {s.archived && <Badge color="zinc">archived</Badge>}
              </SidebarItem>
              <div className="absolute top-1/2 right-1 flex -translate-y-1/2 gap-0.5 opacity-0 group-hover/session:opacity-100">
                <button
                  aria-label={`Rename ${s.title || 'session'}`}
                  onClick={() => {
                    setTitle(s.title)
                    setRenaming(s)
                  }}
                  className="rounded p-1 text-zinc-400 hover:bg-zinc-950/5 hover:text-zinc-600 dark:hover:bg-white/10 dark:hover:text-zinc-300"
                >
                  <PencilIcon className="size-3.5" />
                </button>
                <button
                  aria-label={`${s.archived ? 'Unarchive' : 'Archive'} ${s.title || 'session'}`}
                  onClick={() => void archive(s)}
                  className="rounded p-1 text-zinc-400 hover:bg-zinc-950/5 hover:text-zinc-600 dark:hover:bg-white/10 dark:hover:text-zinc-300"
                >
                  <ArchiveBoxIcon className="size-3.5" />
                </button>
              </div>
            </div>
          ))}
        </SidebarSection>
      ))}

      {sessions.length === 0 && query !== '' && (
        <p className="px-2 py-1 text-xs text-zinc-400 dark:text-zinc-500">No sessions match.</p>
      )}
      {hasMore && <div ref={sentinelRef} className="h-px" data-testid="sessions-sentinel" />}

      <Dialog open={renaming !== null} onClose={() => setRenaming(null)}>
        <DialogTitle>Rename session</DialogTitle>
        <DialogBody>
          <Input
            aria-label="Session title"
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') void rename()
            }}
            autoFocus
          />
        </DialogBody>
        <DialogActions>
          <Button plain onClick={() => setRenaming(null)}>
            Cancel
          </Button>
          <Button onClick={() => void rename()}>Save</Button>
        </DialogActions>
      </Dialog>
    </>
  )
}
