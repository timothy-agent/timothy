import {
  Add01Icon,
  Archive02Icon,
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
  SidebarGroupLabel,
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

  return (
    <>
      <SidebarGroup>
        <SidebarGroupContent>
          <SidebarMenu>
            <SidebarMenuItem>
              <SidebarMenuButton asChild isActive={pathname === '/chat'}>
                <Link to="/chat">
                  <HugeiconsIcon icon={Add01Icon} />
                  <span>New chat</span>
                </Link>
              </SidebarMenuButton>
            </SidebarMenuItem>
          </SidebarMenu>
          <div className="relative mt-1 px-1">
            <HugeiconsIcon icon={Search01Icon} className="pointer-events-none absolute top-1/2 left-3 size-3.5 -translate-y-1/2 text-muted-foreground" />
            <Input
              aria-label="Search sessions"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Search…"
              className="h-8 pl-8"
            />
          </div>
        </SidebarGroupContent>
      </SidebarGroup>

      {groupByDay(sessions).map((group) => (
        <SidebarGroup key={group.label}>
          <SidebarGroupLabel>{group.label}</SidebarGroupLabel>
          <SidebarGroupContent>
            <SidebarMenu>
              {group.sessions.map((s) => (
                <SidebarMenuItem key={s.id}>
                  <SidebarMenuButton asChild isActive={pathname === `/chat/${s.id}`}>
                    <Link to={`/chat/${s.id}`}>
                      <span className="truncate">{s.title || 'New session'}</span>
                      {s.archived && <Badge variant="outline">archived</Badge>}
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
      ))}

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
