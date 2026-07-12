import {
  Add01Icon,
  Archive02Icon,
  BubbleChatIcon,
  MoreHorizontalIcon,
  PencilEdit01Icon,
  Search01Icon,
  Unarchive03Icon,
} from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useEffect, useRef, useState } from 'react'
import { Link, useLocation, useNavigate } from 'react-router'
import { updateSession } from '../api/client'
import type { SessionMeta } from '../api/types'
import { groupByDay, useSessions } from '../lib/sessions'
import { Badge } from './ui/badge'
import { Button } from './ui/button'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from './ui/dialog'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from './ui/dropdown-menu'
import { Input } from './ui/input'
import {
  SidebarGroup,
  SidebarGroupContent,
  SidebarMenu,
  SidebarMenuAction,
  SidebarMenuButton,
  SidebarMenuItem,
} from './ui/sidebar'

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
    if (!s.archived && pathname === `/chat/${s.id}`) navigate('/chat')
  }

  // Flat, dated rows (not day-sectioned): each session carries its own
  // label directly beneath its title, same shape SessionList always
  // had via groupByDay, just rendered per-row instead of per-section.
  const dated = groupByDay(sessions).flatMap((group) =>
    group.sessions.map((s) => ({ session: s, label: group.label })),
  )

  return (
    <>
      <div className="flex items-center justify-between px-3 pt-1 pb-2">
        <span className="text-sm font-semibold tracking-tight">Chats</span>
        <Link
          to="/chat"
          aria-label="New chat"
          className="flex size-7 items-center justify-center rounded-md text-muted-foreground transition hover:bg-zinc-100 hover:text-zinc-900 dark:hover:bg-zinc-800 dark:hover:text-zinc-50"
        >
          <HugeiconsIcon icon={Add01Icon} className="size-4" />
        </Link>
      </div>

      <SidebarGroup className="pt-0">
        <SidebarGroupContent>
          <div className="relative px-1">
            <HugeiconsIcon icon={Search01Icon} className="pointer-events-none absolute top-1/2 left-3 size-3.5 -translate-y-1/2 text-muted-foreground" />
            <Input
              aria-label="Search sessions"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Search chats…"
              className="h-8 pl-8"
            />
          </div>
        </SidebarGroupContent>
      </SidebarGroup>

      <SidebarGroup className="pt-0">
        <SidebarGroupContent>
          <SidebarMenu className="gap-3">
            {dated.map(({ session: s, label }) => (
              <SidebarMenuItem key={s.id}>
                <SidebarMenuButton asChild isActive={pathname === `/chat/${s.id}`} className="h-auto flex-col items-start gap-0.5 py-1.5">
                  <Link to={`/chat/${s.id}`}>
                    <span className="flex w-full items-center gap-1.5">
                      <HugeiconsIcon icon={BubbleChatIcon} className="size-3.5 shrink-0 text-muted-foreground" />
                      <span className="truncate font-medium">{s.title || 'New session'}</span>
                      {s.archived && <Badge variant="outline">archived</Badge>}
                    </span>
                    <span className="pl-5 text-xs text-muted-foreground">{label}</span>
                  </Link>
                </SidebarMenuButton>
                <DropdownMenu>
                  <DropdownMenuTrigger asChild>
                    <SidebarMenuAction showOnHover aria-label={`Actions for ${s.title || 'session'}`}>
                      <HugeiconsIcon icon={MoreHorizontalIcon} />
                    </SidebarMenuAction>
                  </DropdownMenuTrigger>
                  <DropdownMenuContent side="right" align="start">
                    <DropdownMenuItem
                      onClick={() => {
                        setTitle(s.title)
                        setRenaming(s)
                      }}
                    >
                      <HugeiconsIcon icon={PencilEdit01Icon} />
                      Rename
                    </DropdownMenuItem>
                    <DropdownMenuItem onClick={() => void archive(s)}>
                      <HugeiconsIcon icon={s.archived ? Unarchive03Icon : Archive02Icon} />
                      {s.archived ? 'Unarchive' : 'Archive'}
                    </DropdownMenuItem>
                  </DropdownMenuContent>
                </DropdownMenu>
              </SidebarMenuItem>
            ))}
          </SidebarMenu>
        </SidebarGroupContent>
      </SidebarGroup>

      {sessions.length === 0 && query !== '' && (
        <p className="px-4 py-1 text-xs text-muted-foreground">No sessions match.</p>
      )}
      {hasMore && <div ref={sentinelRef} className="h-px" data-testid="sessions-sentinel" />}

      <Dialog open={renaming !== null} onOpenChange={(open) => !open && setRenaming(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Rename session</DialogTitle>
          </DialogHeader>
          <Input
            aria-label="Session title"
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') void rename()
            }}
            autoFocus
          />
          <DialogFooter>
            <Button variant="ghost" onClick={() => setRenaming(null)}>
              Cancel
            </Button>
            <Button onClick={() => void rename()}>Save</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}
