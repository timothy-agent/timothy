import {
  Analytics01Icon,
  Brain02Icon,
  BubbleChatIcon,
  Home01Icon,
  Key01Icon,
  Moon02Icon,
  RocketIcon,
  Search01Icon,
  Settings02Icon,
  Sun03Icon,
} from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link, Navigate, Route, Routes, useLocation, useNavigate, useParams } from 'react-router'
import { Toaster } from 'sonner'
import { getToken } from './api/client'
import { SessionList } from './components/SessionList'
import { SessionsProvider } from './components/SessionsProvider'
import { SettingsDialog } from './components/SettingsDialog'
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
  SidebarProvider,
  SidebarTrigger,
} from './components/ui/sidebar'
import { TooltipProvider } from './components/ui/tooltip'
import { useSessions } from './lib/sessions'
import { usePendingMemories } from './lib/memory'
import { getTheme, nextTheme, setTheme, type Theme } from './lib/theme'
import { Chat } from './pages/Chat'
import { Analytics } from './pages/Analytics'
import { EditSchedule } from './pages/EditSchedule'
import { Home } from './pages/Home'
import { Memory } from './pages/Memory'
import { MissionDetail } from './pages/MissionDetail'
import { Missions } from './pages/Missions'
import { NewMission } from './pages/NewMission'
import { Research } from './pages/Research'
import { Settings } from './pages/Settings'

const nav = [
  { label: 'Home', href: '/', icon: Home01Icon },
  { label: 'Chat', href: '/chat', icon: BubbleChatIcon },
  { label: 'Missions', href: '/missions', icon: RocketIcon },
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
  if (href === '/settings') return pathname.startsWith('/settings')
  return pathname === href
}

// breadcrumbFor turns the current path into the header's breadcrumb
// trail — static per top-level page, with a "Settings / <tab>" split
// for the one section that has sub-pages.
function breadcrumbFor(pathname: string): string[] {
  const match = nav.find((n) => isActive(pathname, n.href))
  if (pathname.startsWith('/settings/')) {
    const tab = pathname.split('/')[2]
    if (tab) return ['Settings', tab.charAt(0).toUpperCase() + tab.slice(1)]
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
  theme,
  onCycleTheme,
  onToken,
}: {
  pendingMemories: number
  theme: Theme
  onCycleTheme: () => void
  onToken: () => void
}) {
  const { pathname } = useLocation()

  return (
    <Sidebar collapsible="icon">
      <SidebarHeader>
        <div className="flex items-center gap-2 px-1 py-1">
          <Link to="/" aria-label="Timothy home" className="flex size-7 shrink-0 items-center justify-center">
            <span className="block size-3.5 rounded-full bg-gradient-to-br from-blue-500 to-violet-600" />
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
              {nav.map((item) => (
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
                </SidebarMenuItem>
              ))}
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
        </SidebarMenu>
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

  useEffect(() => {
    if (getToken() === '') setTokenOpen(true)
  }, [])

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
        <Toaster richColors closeButton theme={theme} />
        <SidebarProvider className="min-h-dvh">
          <AppSidebar
            pendingMemories={pendingMemories}
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
                    <div className="mx-auto flex h-full max-w-4xl flex-col">
                      <Chat onNeedToken={openToken} />
                    </div>
                  }
                />
                <Route
                  path="/research/:id?"
                  element={
                    <div className="mx-auto flex h-full max-w-4xl flex-col">
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
                <Route path="/memory" element={<Memory />} />
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
