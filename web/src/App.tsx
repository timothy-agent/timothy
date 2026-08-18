import {
  Analytics01Icon,
  ArrowRight01Icon,
  Brain02Icon,
  BubbleChatIcon,
  GithubIcon,
  Home01Icon,
  Key01Icon,
  LibraryIcon,
  Moon02Icon,
  RocketIcon,
  Search01Icon,
  Settings02Icon,
  Sun03Icon,
} from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link, Navigate, Route, Routes, useLocation, useNavigate, useParams } from 'react-router'
import { toast, Toaster } from 'sonner'
import { getToken, subscribeNeedToken } from './api/client'
import { BrandMark } from './components/BrandMark'
import { SessionList } from './components/SessionList'
import { SessionsProvider } from './components/SessionsProvider'
import { SettingsDialog } from './components/SettingsDialog'
import { ConnectorLogoSprite } from './components/settings/ConnectorLogo'
import { LogoSprite } from './components/settings/ProviderLogo'
import {
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from './components/ui/command'
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarHeader,
  SidebarInset,
  SidebarMenu,
  SidebarMenuBadge,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarMenuSub,
  SidebarMenuSubButton,
  SidebarMenuSubItem,
  SidebarProvider,
  SidebarTrigger,
  useSidebar,
} from './components/ui/sidebar'
import { TooltipProvider } from './components/ui/tooltip'
import { cn } from './lib/utils'
import { useSessions } from './lib/sessions'
import { usePendingMemories } from './lib/memory'
import { playAlertSound, unlockAudio } from './lib/alertSound'
import { newlySeen, usePendingPermissions } from './lib/permissions'
import { getNotificationSoundEnabled } from './lib/sound'
import { getTheme, nextTheme, setTheme, type Theme } from './lib/theme'
import { Chat } from './pages/Chat'
import { Analytics } from './pages/Analytics'
import { EditSchedule } from './pages/EditSchedule'
import { Home } from './pages/Home'
import { Knowledge } from './pages/Knowledge'
import { KnowledgeRedirect, Memory } from './pages/Memory'
import { MissionDetail } from './pages/MissionDetail'
import { Missions } from './pages/Missions'
import { NewMission } from './pages/NewMission'
import { Research } from './pages/Research'
import { Settings, settingsAreas } from './pages/Settings'

const nav = [
  { label: 'Home', href: '/', icon: Home01Icon },
  { label: 'Chat', href: '/chat', icon: BubbleChatIcon },
  { label: 'Missions', href: '/missions', icon: RocketIcon },
  { label: 'Knowledge', href: '/knowledge', icon: LibraryIcon },
  { label: 'Memory', href: '/memory', icon: Brain02Icon },
  { label: 'Analytics', href: '/analytics', icon: Analytics01Icon },
  { label: 'Settings', href: '/settings', icon: Settings02Icon },
]

const themeIcon = { system: Sun03Icon, light: Sun03Icon, dark: Moon02Icon }
const themeLabel = { system: 'System theme', light: 'Light theme', dark: 'Dark theme' }

function isActive(pathname: string, href: string): boolean {
  if (href === '/') return pathname === '/'
  if (href === '/chat') return pathname === '/chat' || pathname.startsWith('/chat/')
  if (href === '/missions') return pathname === '/missions' || pathname.startsWith('/missions/')
  if (href === '/knowledge') return pathname === '/knowledge' || pathname.startsWith('/knowledge/')
  if (href === '/settings') return pathname.startsWith('/settings')
  return pathname === href
}

// breadcrumbFor turns the current path into the header's breadcrumb
// trail — static per top-level page, with a "Settings / <area>" split
// for the one section that has sub-pages.
function breadcrumbFor(pathname: string): string[] {
  const match = nav.find((n) => isActive(pathname, n.href))
  if (pathname.startsWith('/settings/')) {
    const key = pathname.split('/')[2]
    const area = settingsAreas.find((a) => a.key === key)
    if (area) return ['Settings', area.label]
  }
  return [match?.label ?? 'Timothy']
}

// AppSidebar is the persistent left navigation: icon-collapsible via
// the shadcn Sidebar primitive, a flat destination list (this app has
// no "workspace" concept to group under), and the chat history panel
// beneath it — the same list SessionList always rendered, now living
// in a real collapsible Sidebar instead of a hand-rolled drawer.
function AppSidebar({
  pendingMemories,
  pendingPermissions,
  theme,
  onCycleTheme,
  onToken,
}: {
  pendingMemories: number
  pendingPermissions: number
  theme: Theme
  onCycleTheme: () => void
  onToken: () => void
}) {
  const { pathname } = useLocation()
  const navigate = useNavigate()
  const { state: sidebarState, isMobile } = useSidebar()
  // Settings starts expanded whenever we're already on a settings
  // route (deep link or in-app nav), and stays however the user last
  // toggled it otherwise — same "sticky until touched" feel as the
  // rest of the sidebar's collapse state.
  const [settingsOpen, setSettingsOpen] = useState(() => pathname.startsWith('/settings'))
  useEffect(() => {
    if (pathname.startsWith('/settings')) setSettingsOpen(true)
  }, [pathname])
  // Icon-collapsed mode hides the submenu entirely (no room for it),
  // so a click there jumps straight to the first area instead of
  // toggling an invisible expand state.
  const iconCollapsed = sidebarState === 'collapsed' && !isMobile

  return (
    <Sidebar collapsible="icon">
      <SidebarHeader>
        <div className="flex items-center gap-2 px-1 py-1">
          <Link to="/" aria-label="Timothy home" className="flex size-7 shrink-0 items-center justify-center">
            <BrandMark className="size-5" />
          </Link>
          <span className="truncate text-sm font-semibold tracking-tight group-data-[collapsible=icon]:hidden">
            Timothy
          </span>
        </div>
      </SidebarHeader>

      <SidebarContent>
        <SidebarGroup>
          <SidebarGroupContent>
            <SidebarMenu>
              {nav.map((item) =>
                item.href === '/settings' ? (
                  <SidebarMenuItem key={item.href}>
                    <SidebarMenuButton
                      isActive={isActive(pathname, item.href)}
                      tooltip={item.label}
                      onClick={() =>
                        iconCollapsed
                          ? navigate(`/settings/${settingsAreas[0].key}`)
                          : setSettingsOpen((open) => !open)
                      }
                    >
                      <HugeiconsIcon icon={item.icon} />
                      <span>{item.label}</span>
                      <HugeiconsIcon
                        icon={ArrowRight01Icon}
                        className={cn(
                          'ml-auto size-3.5! transition-transform group-data-[collapsible=icon]:hidden',
                          settingsOpen && 'rotate-90',
                        )}
                      />
                    </SidebarMenuButton>
                    {settingsOpen && (
                      <SidebarMenuSub>
                        {settingsAreas.map((area) => (
                          <SidebarMenuSubItem key={area.key}>
                            <SidebarMenuSubButton
                              asChild
                              isActive={pathname.startsWith(`/settings/${area.key}`)}
                            >
                              <Link to={`/settings/${area.key}`}>
                                <span>{area.label}</span>
                              </Link>
                            </SidebarMenuSubButton>
                          </SidebarMenuSubItem>
                        ))}
                      </SidebarMenuSub>
                    )}
                  </SidebarMenuItem>
                ) : (
                  <SidebarMenuItem key={item.href}>
                    <SidebarMenuButton asChild isActive={isActive(pathname, item.href)} tooltip={item.label}>
                      <Link to={item.href}>
                        <HugeiconsIcon icon={item.icon} />
                        <span>{item.label}</span>
                      </Link>
                    </SidebarMenuButton>
                    {item.href === '/memory' && pendingMemories > 0 && (
                      <SidebarMenuBadge>{pendingMemories}</SidebarMenuBadge>
                    )}
                    {item.href === '/chat' && pendingPermissions > 0 && (
                      <SidebarMenuBadge>{pendingPermissions}</SidebarMenuBadge>
                    )}
                  </SidebarMenuItem>
                ),
              )}
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>

        <div className="flex min-h-0 flex-1 flex-col overflow-y-auto group-data-[collapsible=icon]:hidden">
          <SessionList />
        </div>
      </SidebarContent>

      <SidebarFooter>
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton onClick={onCycleTheme} tooltip={themeLabel[theme]}>
              <HugeiconsIcon icon={themeIcon[theme]} />
              <span>{themeLabel[theme]}</span>
            </SidebarMenuButton>
          </SidebarMenuItem>
          <SidebarMenuItem>
            <SidebarMenuButton onClick={onToken} tooltip="API token">
              <HugeiconsIcon icon={Key01Icon} />
              <span>API token</span>
            </SidebarMenuButton>
          </SidebarMenuItem>
          <SidebarMenuItem>
            <SidebarMenuButton asChild tooltip="View on GitHub">
              <a href="https://github.com/timothy-agent/timothy" target="_blank" rel="noreferrer">
                <HugeiconsIcon icon={GithubIcon} />
                <span>GitHub</span>
              </a>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
        <div className="px-2 py-1 text-xs text-muted-foreground group-data-[collapsible=icon]:hidden">
          v{__APP_VERSION__} ({__GIT_SHA__})
        </div>
      </SidebarFooter>
    </Sidebar>
  )
}

// TopBar: sidebar collapse trigger, route breadcrumb, and the cmd-K
// launcher — the one persistent strip above every page.
function TopBar({ onOpenPalette }: { onOpenPalette: () => void }) {
  const { pathname } = useLocation()
  const crumbs = breadcrumbFor(pathname)
  const isMac = typeof navigator !== 'undefined' && /mac/i.test(navigator.platform)

  return (
    <header className="flex h-12 w-full shrink-0 items-center gap-3 border-b border-border px-3">
      <SidebarTrigger />
      <nav aria-label="Breadcrumb" className="flex min-w-0 items-center gap-1.5 text-sm">
        {crumbs.map((c, i) => (
          <span key={c} className="flex items-center gap-1.5">
            {i > 0 && <span className="text-muted-foreground/60">/</span>}
            <span className={i === crumbs.length - 1 ? 'font-semibold' : 'text-muted-foreground'}>{c}</span>
          </span>
        ))}
      </nav>
      <button
        type="button"
        onClick={onOpenPalette}
        aria-label="Search or jump to…"
        className="ml-auto flex min-w-52 items-center gap-2 rounded-lg border border-border bg-card px-2.5 py-1.5 text-xs text-muted-foreground transition hover:border-zinc-400 dark:hover:border-zinc-600"
      >
        <HugeiconsIcon icon={Search01Icon} className="size-3.5" />
        <span>Search or jump to…</span>
        <kbd className="ml-auto rounded border border-border bg-background px-1 py-px font-mono text-[10px]">
          {isMac ? '⌘K' : 'Ctrl K'}
        </kbd>
      </button>
    </header>
  )
}

// CommandPalette: cmd/ctrl+K launcher for page navigation and jumping
// straight into a recent session, so switching context never requires
// hunting through the sidebar.
function CommandPalette({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const navigate = useNavigate()
  const { sessions } = useSessions()
  const recent = useMemo(() => sessions.slice(0, 8), [sessions])

  const go = (href: string) => {
    onOpenChange(false)
    navigate(href)
  }

  return (
    <CommandDialog open={open} onOpenChange={onOpenChange}>
      <CommandInput placeholder="Jump to a page or a recent chat…" />
      <CommandList>
        <CommandEmpty>No matches.</CommandEmpty>
        <CommandGroup heading="Pages">
          {nav.map((item) => (
            <CommandItem key={item.href} value={item.label} onSelect={() => go(item.href)}>
              <HugeiconsIcon icon={item.icon} />
              <span>{item.label}</span>
            </CommandItem>
          ))}
        </CommandGroup>
        {recent.length > 0 && (
          <CommandGroup heading="Recent chats">
            {recent.map((s) => (
              <CommandItem
                key={s.id}
                value={s.title || 'New session'}
                onSelect={() => go(`/chat/${s.id}`)}
              >
                <HugeiconsIcon icon={BubbleChatIcon} />
                <span className="truncate">{s.title || 'New session'}</span>
              </CommandItem>
            ))}
          </CommandGroup>
        )}
      </CommandList>
    </CommandDialog>
  )
}

// LegacySessionRedirect keeps old /sessions/{id} links working.
function LegacySessionRedirect() {
  const { id } = useParams()
  return <Navigate to={`/chat/${id}`} replace />
}

function App() {
  const [tokenOpen, setTokenOpen] = useState(false)
  const [paletteOpen, setPaletteOpen] = useState(false)
  const [theme, setThemeState] = useState<Theme>(() => getTheme())
  const pendingMemories = usePendingMemories()
  const pendingPermissions = usePendingPermissions()
  const navigate = useNavigate()

  useEffect(() => {
    if (getToken() === '') setTokenOpen(true)
  }, [])

  useEffect(() => {
    let lastToast = 0
    return subscribeNeedToken(() => {
      setTokenOpen(true)
      const now = Date.now()
      if (now - lastToast < 4000) return
      lastToast = now
      toast.error("Timothy's API token is missing or invalid", {
        description: 'Paste TIMOTHY_API_TOKEN from deploy/.env. This is not an LLM provider key.',
      })
    })
  }, [])

  // Primes the shared AudioContext on the app's FIRST real user
  // gesture: a permission toast can fire from a background SSE signal
  // with no gesture of its own, and autoplay policy silently blocks a
  // chime that never rode an unlocked context — one-shot, removes
  // itself after the first fire so it's not doing work on every click.
  useEffect(() => {
    const unlock = () => {
      unlockAudio()
      window.removeEventListener('pointerdown', unlock)
      window.removeEventListener('keydown', unlock)
    }
    window.addEventListener('pointerdown', unlock)
    window.addEventListener('keydown', unlock)
    return () => {
      window.removeEventListener('pointerdown', unlock)
      window.removeEventListener('keydown', unlock)
    }
  }, [])

  // Toast + sound fire only for a NEWLY seen pending permission (a
  // session_id not present on the previous render) — every poll/signal
  // refetch would otherwise re-toast the same still-pending ask.
  const seenPermissions = useRef<Set<string>>(new Set())
  useEffect(() => {
    const fresh = newlySeen(seenPermissions.current, pendingPermissions)
    const current = new Set(pendingPermissions.map((p) => p.session_id))
    if (fresh.length > 0) {
      if (getNotificationSoundEnabled()) playAlertSound()
      for (const p of fresh) {
        toast(`${p.tool} needs your approval`, {
          description: p.session_title || 'Untitled session',
          duration: Infinity,
          action: {
            label: 'Review',
            onClick: () => navigate(`/chat/${p.session_id}`),
          },
        })
      }
    }
    seenPermissions.current = current
  }, [pendingPermissions, navigate])

  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        setPaletteOpen((v) => !v)
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [])

  // Stable identity: Chat's resume effect depends on it.
  const openToken = useCallback(() => setTokenOpen(true), [])

  const cycleTheme = () => {
    const t = nextTheme[theme]
    setTheme(t)
    setThemeState(t)
  }

  return (
    <TooltipProvider delayDuration={300}>
      <SessionsProvider>
        <LogoSprite />
        <ConnectorLogoSprite />
        <Toaster richColors closeButton theme={theme} />
        <SidebarProvider className="min-h-dvh">
          <AppSidebar
            pendingMemories={pendingMemories}
            pendingPermissions={pendingPermissions.length}
            theme={theme}
            onCycleTheme={cycleTheme}
            onToken={openToken}
          />
          <SidebarInset className="bg-dot-grid">
            <TopBar onOpenPalette={() => setPaletteOpen(true)} />
            <div className="min-h-0 flex-1 overflow-hidden px-4">
              <Routes>
                <Route path="/" element={<Home />} />
                {/* One route pattern serves new chats and resumes:
                    switching between them must re-render, not remount,
                    so an in-flight stream survives adopting its new
                    session URL. */}
                <Route
                  path="/chat/:id?"
                  element={
                    <div className="mx-auto flex h-full w-full max-w-full flex-col px-8">
                      <Chat onNeedToken={openToken} />
                    </div>
                  }
                />
                <Route
                  path="/research/:id?"
                  element={
                    <div className="mx-auto flex h-full w-full max-w-full flex-col px-8">
                      <Research onNeedToken={openToken} />
                    </div>
                  }
                />
                <Route path="/sessions/:id" element={<LegacySessionRedirect />} />
                <Route path="/analytics" element={<Analytics />} />
                {/* Old bookmarks: the page lived at /dashboard before the rename. */}
                <Route path="/dashboard" element={<Navigate to="/analytics" replace />} />
                <Route path="/missions" element={<Missions />} />
                <Route path="/missions/new" element={<NewMission />} />
                <Route path="/missions/schedules/:id/edit" element={<EditSchedule />} />
                <Route path="/missions/:id" element={<MissionDetail />} />
                <Route path="/knowledge/*" element={<Knowledge />} />
                <Route path="/memory/knowledge/*" element={<KnowledgeRedirect />} />
                <Route path="/memory/*" element={<Memory />} />
                <Route path="/settings/*" element={<Settings />} />
                {/* Old bookmark: Settings lived at one page with ?tab= before sub-routes. */}
                <Route path="/settings" element={<Navigate to="/settings/providers" replace />} />
              </Routes>
            </div>
            <SettingsDialog open={tokenOpen} onClose={() => setTokenOpen(false)} />
          </SidebarInset>
        </SidebarProvider>
        <CommandPalette open={paletteOpen} onOpenChange={setPaletteOpen} />
      </SessionsProvider>
    </TooltipProvider>
  )
}

export default App
