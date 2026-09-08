import { Brain, ChartColumn, ChevronRight, House, KeyRound, Library, MessageCircle, Moon, Repeat, Rocket, Search, Settings as SettingsIcon, Sun } from 'lucide-react'
import { lazy, Suspense, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link, Navigate, Route, Routes, useLocation, useNavigate, useParams } from 'react-router'
import { toast, Toaster } from 'sonner'
import { getToken, subscribeNeedToken } from './api/client'
import { BrandMark } from './components/BrandMark'
import { SessionList } from './components/SessionList'
import { SessionsProvider } from './components/SessionsProvider'
import { SettingsDialog } from './components/SettingsDialog'
import { ConnectorLogoSprite } from './components/settings/ConnectorLogo'
import { LogoSprite } from './components/timothy/provider-logo'
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
import { newlySeen, toastSessionLabel, usePendingPermissions } from './lib/permissions'
import { getNotificationSoundEnabled } from './lib/sound'
import { getTheme, nextTheme, setTheme, type Theme } from './lib/theme'
import { Chat } from './pages/Chat'
import { AutomationDetail } from './pages/AutomationDetail'
import { Automations } from './pages/Automations'
import { EditSchedule } from './pages/EditSchedule'
import { Home } from './pages/Home'
import { Knowledge } from './pages/Knowledge'
import { KnowledgeRedirect, Memory } from './pages/Memory'
import { MissionDetail } from './pages/MissionDetail'
import { Missions } from './pages/Missions'
import { NewMission } from './pages/NewMission'
import { Research } from './pages/Research'
import { Settings, settingsAreas } from './pages/Settings'
import { DesignSystem } from './pages/DesignSystem'

// Analytics pulls in ECharts (a large dependency), so it stays a
// lazily-loaded chunk rather than bundling into the initial app load.
const Analytics = lazy(() => import('./pages/Analytics').then((m) => ({ default: m.Analytics })))

const nav = [
  { label: 'Home', href: '/', icon: House },
  { label: 'Chat', href: '/chat', icon: MessageCircle },
  { label: 'Missions', href: '/missions', icon: Rocket },
  { label: 'Automations', href: '/automations', icon: Repeat },
  { label: 'Knowledge', href: '/knowledge', icon: Library },
  { label: 'Memory', href: '/memory', icon: Brain },
  { label: 'Analytics', href: '/analytics', icon: ChartColumn },
  { label: 'Settings', href: '/settings', icon: SettingsIcon },
]

const themeIcon = { system: Sun, light: Sun, dark: Moon }
const themeLabel = { system: 'System theme', light: 'Light theme', dark: 'Dark theme' }

function isActive(pathname: string, href: string): boolean {
  if (href === '/') return pathname === '/'
  if (href === '/chat') return pathname === '/chat' || pathname.startsWith('/chat/')
  if (href === '/missions') return pathname === '/missions' || pathname.startsWith('/missions/')
  if (href === '/automations') return pathname === '/automations' || pathname.startsWith('/automations/')
  if (href === '/knowledge') return pathname === '/knowledge' || pathname.startsWith('/knowledge/')
  if (href === '/settings') return pathname.startsWith('/settings')
  return pathname === href
}

// breadcrumbFor turns the current path into the header's breadcrumb
// trail, static per top-level page. Settings sub-pages render their
// own breadcrumb trail in their own PageHeader, so this stays a flat
// single crumb like every other top-level page.
function breadcrumbFor(pathname: string): string[] {
  const match = nav.find((n) => isActive(pathname, n.href))
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
  const ThemeIcon = themeIcon[theme]
  // Settings starts expanded when the app loads into a settings route
  // (deep link or a fresh load), then stays however the user toggles
  // it from there, same "sticky until touched" feel as the rest of
  // the sidebar's collapse state.
  const [settingsOpen, setSettingsOpen] = useState(() => pathname.startsWith('/settings'))
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
          <span className="glow-text truncate font-mono text-sm font-semibold tracking-[0.2em] text-brand-text uppercase transition-[opacity,visibility] duration-150 ease-out group-data-[collapsible=icon]:invisible group-data-[collapsible=icon]:opacity-0">
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
                      aria-expanded={iconCollapsed ? undefined : settingsOpen}
                      onClick={() =>
                        iconCollapsed
                          ? navigate(`/settings/${settingsAreas[0].key}`)
                          : setSettingsOpen((open) => !open)
                      }
                    >
                      <item.icon />
                      <span>{item.label}</span>
                      <ChevronRight className={cn(
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
                        <item.icon />
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

        <div className="flex min-h-0 flex-1 flex-col overflow-y-auto transition-[opacity,visibility] duration-150 ease-out group-data-[collapsible=icon]:invisible group-data-[collapsible=icon]:opacity-0">
          <SessionList />
        </div>
      </SidebarContent>

      <SidebarFooter>
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton onClick={onCycleTheme} tooltip={themeLabel[theme]}>
              <ThemeIcon />
              <span>{themeLabel[theme]}</span>
            </SidebarMenuButton>
          </SidebarMenuItem>
          <SidebarMenuItem>
            <SidebarMenuButton onClick={onToken} tooltip="API token">
              <KeyRound />
              <span>API token</span>
            </SidebarMenuButton>
          </SidebarMenuItem>
          <SidebarMenuItem>
            <SidebarMenuButton asChild tooltip="View on GitHub">
              <a href="https://github.com/timothy-agent/timothy" target="_blank" rel="noreferrer">
                <svg className="size-4 fill-current" aria-hidden="true">
                  <use href="#clogo-github" />
                </svg>
                <span>GitHub</span>
              </a>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
        <div className="whitespace-nowrap px-2 py-1 text-xs text-muted-foreground transition-[opacity,visibility] duration-150 ease-out group-data-[collapsible=icon]:invisible group-data-[collapsible=icon]:opacity-0">
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
    <header className="sticky top-0 z-20 flex h-12 w-full shrink-0 items-center gap-3 border-b border-border bg-background px-3">
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
        <Search className="size-3.5" />
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
              <item.icon />
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
                <MessageCircle />
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

// LegacyEditScheduleRedirect keeps old /missions/schedules/{id}/edit
// links working now that schedule editing lives under /automations.
function LegacyEditScheduleRedirect() {
  const { id } = useParams()
  return <Navigate to={`/automations/${id}/edit`} replace />
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
          description: toastSessionLabel(p),
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
          <SidebarInset className="min-w-0 bg-dot-grid">
            <TopBar onOpenPalette={() => setPaletteOpen(true)} />
            <div className="min-h-0 min-w-0 flex-1 overflow-hidden px-4">
              <Routes>
                <Route path="/" element={<Home />} />
                {/* One route pattern serves new chats and resumes:
                    switching between them must re-render, not remount,
                    so an in-flight stream survives adopting its new
                    session URL. */}
                <Route
                  path="/chat/:id?"
                  element={
                    <div className="mx-auto flex h-full w-full max-w-full flex-col px-4">
                      <Chat onNeedToken={openToken} />
                    </div>
                  }
                />
                <Route
                  path="/research/:id?"
                  element={
                    <div className="mx-auto flex h-full w-full max-w-full flex-col px-4">
                      <Research onNeedToken={openToken} />
                    </div>
                  }
                />
                <Route path="/sessions/:id" element={<LegacySessionRedirect />} />
                <Route
                  path="/analytics"
                  element={
                    <Suspense fallback={<div className="p-8 text-sm text-muted-foreground">Loading…</div>}>
                      <Analytics />
                    </Suspense>
                  }
                />
                {/* Old bookmarks: the page lived at /dashboard before the rename. */}
                <Route path="/dashboard" element={<Navigate to="/analytics" replace />} />
                <Route path="/missions" element={<Missions />} />
                <Route path="/missions/new" element={<NewMission />} />
                {/* Old bookmark: schedule editing lived under /missions before automations got their own page. */}
                <Route
                  path="/missions/schedules/:id/edit"
                  element={<LegacyEditScheduleRedirect />}
                />
                <Route path="/missions/:id" element={<MissionDetail />} />
                <Route path="/automations" element={<Automations />} />
                <Route path="/automations/:id/edit" element={<EditSchedule />} />
                <Route path="/automations/:id" element={<AutomationDetail />} />
                <Route path="/knowledge/*" element={<Knowledge />} />
                <Route path="/memory/knowledge/*" element={<KnowledgeRedirect />} />
                <Route path="/memory/*" element={<Memory />} />
                <Route path="/settings/*" element={<Settings />} />
                {/* Old bookmark: Settings lived at one page with ?tab= before sub-routes. */}
                <Route path="/settings" element={<Navigate to="/settings/providers" replace />} />
                <Route path="/design" element={<DesignSystem />} />
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
