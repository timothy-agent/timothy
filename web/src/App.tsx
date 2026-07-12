import {
  Analytics01Icon,
  ArrowLeft01Icon,
  Brain02Icon,
  BubbleChatIcon,
  Cancel01Icon,
  ComputerIcon,
  Home01Icon,
  Key01Icon,
  Menu01Icon,
  Moon02Icon,
  PlusSignIcon,
  Settings02Icon,
  Sun03Icon,
} from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useCallback, useEffect, useState } from 'react'
import { Link, Navigate, Route, Routes, useLocation, useNavigate, useParams } from 'react-router'
import { getToken } from './api/client'
import { SessionList } from './components/SessionList'
import { SessionsProvider } from './components/SessionsProvider'
import { SettingsDialog } from './components/SettingsDialog'
import {
  SidebarGroup,
  SidebarGroupContent,
  SidebarMenu,
  SidebarMenuBadge,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarProvider,
  useSidebar,
} from './components/ui/sidebar'
import { usePendingMemories } from './lib/memory'
import { getTheme, nextTheme, setTheme, type Theme } from './lib/theme'
import { cn } from './lib/utils'
import { Chat } from './pages/Chat'
import { Dashboard } from './pages/Dashboard'
import { Home } from './pages/Home'
import { Memory } from './pages/Memory'
import { Settings } from './pages/Settings'

const railNav = [
  { label: 'Home', href: '/', icon: Home01Icon },
  { label: 'Chat', href: '/chat', icon: BubbleChatIcon },
  { label: 'Memory', href: '/memory', icon: Brain02Icon },
  { label: 'Dashboard', href: '/dashboard', icon: Analytics01Icon },
  { label: 'Settings', href: '/settings', icon: Settings02Icon },
]

const themeIcon = { system: ComputerIcon, light: Sun03Icon, dark: Moon02Icon }
const themeLabel = { system: 'System theme', light: 'Light theme', dark: 'Dark theme' }

function isActive(pathname: string, href: string): boolean {
  if (href === '/') return pathname === '/'
  if (href === '/chat') return pathname === '/chat' || pathname.startsWith('/chat/')
  return pathname === href
}

// TopBar is the one persistent strip above everything: logo, back (only
// meaningful inside a chat session), and the history toggle. The
// toggle works from any page — it drives the same sidebar the history
// panel renders in, via useSidebar, so it opens the mobile sheet or
// the desktop panel correctly without tracking its own open state.
function TopBar() {
  const { pathname } = useLocation()
  const navigate = useNavigate()
  const { toggleSidebar, open, openMobile, isMobile } = useSidebar()
  const historyOpen = isMobile ? openMobile : open
  const inSession = pathname.startsWith('/chat/')

  return (
    <header className="flex h-12 w-full shrink-0 items-center gap-1 border-b border-border px-3">
      <Link to="/" aria-label="Timothy home" className="mr-1 flex size-8 items-center justify-center">
        <span className="size-3 rounded-full bg-gradient-to-br from-blue-500 to-violet-600" />
      </Link>
      {inSession && (
        <button
          type="button"
          onClick={() => navigate('/')}
          aria-label="Back to home"
          className="flex size-8 items-center justify-center rounded-md text-zinc-500 transition hover:bg-zinc-100 hover:text-zinc-900 dark:text-zinc-400 dark:hover:bg-zinc-800 dark:hover:text-zinc-100"
        >
          <HugeiconsIcon icon={ArrowLeft01Icon} className="size-4" />
        </button>
      )}
      <button
        type="button"
        onClick={toggleSidebar}
        aria-label={historyOpen ? 'Hide chat history' : 'Show chat history'}
        aria-pressed={historyOpen}
        className={cn(
          'flex size-8 items-center justify-center rounded-md transition',
          historyOpen
            ? 'bg-zinc-200/70 text-zinc-900 dark:bg-zinc-800 dark:text-zinc-50'
            : 'text-zinc-500 hover:bg-zinc-100 hover:text-zinc-900 dark:text-zinc-400 dark:hover:bg-zinc-800 dark:hover:text-zinc-100',
        )}
      >
        <HugeiconsIcon icon={Menu01Icon} className="size-4" />
      </button>
    </header>
  )
}

// Rail is the desktop workspace navigation: a slim icon column below
// the top bar, every destination one glance away. Mobile navigation
// lives in the sheet the top bar's toggle opens.
function Rail({
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

  const itemClass = (active: boolean) =>
    cn(
      'flex w-full flex-col items-center gap-1 rounded-lg py-2 text-[10px] font-medium transition',
      active
        ? 'bg-zinc-200/70 text-zinc-900 dark:bg-zinc-800 dark:text-zinc-50'
        : 'text-zinc-500 hover:text-zinc-900 dark:text-zinc-400 dark:hover:text-zinc-100',
    )

  return (
    <nav
      aria-label="Workspace"
      className="hidden w-18 shrink-0 flex-col items-center gap-1 border-r border-border px-2 py-3 md:flex"
    >
      <Link
        to="/chat"
        aria-label="New chat"
        className="mb-2 flex size-10 items-center justify-center rounded-xl border border-border text-zinc-600 transition hover:bg-zinc-100 dark:text-zinc-300 dark:hover:bg-zinc-800"
      >
        <HugeiconsIcon icon={PlusSignIcon} className="size-5" />
      </Link>
      {railNav.map((item) => (
        <Link key={item.href} to={item.href} className={itemClass(isActive(pathname, item.href))}>
          <span className="relative">
            <HugeiconsIcon icon={item.icon} className="size-5" />
            {item.href === '/memory' && pendingMemories > 0 && (
              <span
                data-testid="memory-badge"
                className="absolute -top-1.5 -right-2 flex h-4 min-w-4 items-center justify-center rounded-full bg-blue-600 px-1 text-[9px] font-medium text-white"
              >
                {pendingMemories}
              </span>
            )}
          </span>
          {item.label}
        </Link>
      ))}

      <div className="mt-auto flex w-full flex-col items-center gap-1">
        <button type="button" onClick={onCycleTheme} className={itemClass(false)} aria-label={themeLabel[theme]}>
          <HugeiconsIcon icon={themeIcon[theme]} className="size-5" />
          Theme
        </button>
        <button type="button" onClick={onToken} className={itemClass(false)} aria-label="API token">
          <HugeiconsIcon icon={Key01Icon} className="size-5" />
          Token
        </button>
      </div>
    </nav>
  )
}

// LegacySessionRedirect keeps old /sessions/{id} links working.
function LegacySessionRedirect() {
  const { id } = useParams()
  return <Navigate to={`/chat/${id}`} replace />
}

function App() {
  const [tokenOpen, setTokenOpen] = useState(false)
  const [theme, setThemeState] = useState<Theme>(() => getTheme())
  const { pathname } = useLocation()
  const pendingMemories = usePendingMemories()

  useEffect(() => {
    if (getToken() === '') setTokenOpen(true)
  }, [])

  // Stable identity: Chat's resume effect depends on it.
  const openToken = useCallback(() => setTokenOpen(true), [])

  const cycleTheme = () => {
    const t = nextTheme[theme]
    setTheme(t)
    setThemeState(t)
  }

  return (
    <SessionsProvider>
      {/* The history panel is a global overlay: SidebarProvider wraps
          the top bar too so its toggle button can drive the same
          sidebar state via useSidebar, on both desktop and mobile.
          Closes on every navigation — opening it again is always an
          explicit click, never state carried from a previous page. */}
      <SidebarProvider defaultOpen={false} className="flex h-dvh w-full flex-col">
        <RouteClosesHistory />
        <TopBar />
        <div className="flex min-h-0 flex-1">
          <Rail
            pendingMemories={pendingMemories}
            theme={theme}
            onCycleTheme={cycleTheme}
            onToken={openToken}
          />
          <HistoryPanel>
            <SidebarGroup className="md:hidden">
              <SidebarGroupContent>
                <SidebarMenu>
                  {railNav.map((item) => (
                    <SidebarMenuItem key={item.href}>
                      <SidebarMenuButton asChild isActive={isActive(pathname, item.href)}>
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
                  <SidebarMenuItem>
                    <SidebarMenuButton onClick={cycleTheme}>
                      <HugeiconsIcon icon={themeIcon[theme]} />
                      <span>{themeLabel[theme]}</span>
                    </SidebarMenuButton>
                  </SidebarMenuItem>
                  <SidebarMenuItem>
                    <SidebarMenuButton onClick={() => setTokenOpen(true)}>
                      <HugeiconsIcon icon={Key01Icon} />
                      <span>API token</span>
                    </SidebarMenuButton>
                  </SidebarMenuItem>
                </SidebarMenu>
              </SidebarGroupContent>
            </SidebarGroup>
            <SessionList />
          </HistoryPanel>

          <div className="flex min-h-0 min-w-0 flex-1 flex-col">
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
                <Route path="/sessions/:id" element={<LegacySessionRedirect />} />
                <Route path="/dashboard" element={<Dashboard />} />
                <Route path="/memory" element={<Memory />} />
                <Route path="/settings" element={<Settings />} />
              </Routes>
            </div>
            <SettingsDialog open={tokenOpen} onClose={() => setTokenOpen(false)} />
          </div>
        </div>
      </SidebarProvider>
    </SessionsProvider>
  )
}

// HistoryPanel is a plain flex drawer, not the shadcn Sidebar: that
// primitive is fixed-position and assumes it owns the full viewport
// height, which fights a persistent TopBar above it. This one just
// renders inline in the flex row beside Rail, confined to the space
// below the top bar like everything else on the page.
function HistoryPanel({ children }: { children: React.ReactNode }) {
  const { open, openMobile, isMobile, toggleSidebar } = useSidebar()
  const panelOpen = isMobile ? openMobile : open
  if (!panelOpen) return null

  return (
    <aside
      aria-label="Chat history"
      className={cn(
        'flex h-full flex-col overflow-y-auto border-r border-border bg-sidebar text-sidebar-foreground',
        isMobile ? 'w-full' : 'w-64 shrink-0',
      )}
    >
      {isMobile && (
        <div className="flex items-center justify-end px-2 py-1.5">
          <button
            type="button"
            onClick={toggleSidebar}
            aria-label="Close chat history"
            className="flex size-8 items-center justify-center rounded-md text-muted-foreground transition hover:bg-zinc-100 hover:text-zinc-900 dark:hover:bg-zinc-800 dark:hover:text-zinc-50"
          >
            <HugeiconsIcon icon={Cancel01Icon} className="size-4" />
          </button>
        </div>
      )}
      {children}
    </aside>
  )
}

// RouteClosesHistory closes the history panel on every navigation —
// opening it is always an explicit click on the top bar's toggle,
// never state carried over from a previous page.
function RouteClosesHistory() {
  const { pathname } = useLocation()
  const { setOpen, setOpenMobile } = useSidebar()

  useEffect(() => {
    setOpen(false)
    setOpenMobile(false)
    // Route changes alone close the panel; opening it is never part of
    // the effect that reacts to them.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pathname])

  return null
}

export default App
