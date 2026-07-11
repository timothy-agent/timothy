import {
  Analytics01Icon,
  Brain02Icon,
  BubbleChatIcon,
  ComputerIcon,
  Home01Icon,
  Image01Icon,
  InboxIcon,
  Key01Icon,
  Layers01Icon,
  Moon02Icon,
  MoreHorizontalIcon,
  PlusSignIcon,
  Settings02Icon,
  Sun03Icon,
} from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useCallback, useEffect, useState } from 'react'
import { Link, Navigate, Route, Routes, useLocation, useParams } from 'react-router'
import { getToken } from './api/client'
import { SessionList } from './components/SessionList'
import { SessionsProvider } from './components/SessionsProvider'
import { SettingsDialog } from './components/SettingsDialog'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from './components/ui/dropdown-menu'
import {
  Sidebar,
  SidebarContent,
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
import { usePendingMemories } from './lib/memory'
import { getTheme, nextTheme, setTheme, type Theme } from './lib/theme'
import { cn } from './lib/utils'
import { Chat } from './pages/Chat'
import { Dashboard } from './pages/Dashboard'
import { Home } from './pages/Home'
import { Memory } from './pages/Memory'
import { Settings } from './pages/Settings'
import { Lanes, Library, Queues } from './pages/Stubs'

const railNav = [
  { label: 'Home', href: '/', icon: Home01Icon },
  { label: 'Chat', href: '/chat', icon: BubbleChatIcon },
  { label: 'Memory', href: '/memory', icon: Brain02Icon },
  { label: 'Dashboard', href: '/dashboard', icon: Analytics01Icon },
]

const moreNav = [
  { label: 'Lanes', href: '/lanes', icon: Layers01Icon },
  { label: 'Library', href: '/library', icon: Image01Icon },
  { label: 'Queues', href: '/queues', icon: InboxIcon },
  { label: 'Settings', href: '/settings', icon: Settings02Icon },
]

const themeIcon = { system: ComputerIcon, light: Sun03Icon, dark: Moon02Icon }
const themeLabel = { system: 'System theme', light: 'Light theme', dark: 'Dark theme' }

function isActive(pathname: string, href: string): boolean {
  if (href === '/') return pathname === '/'
  if (href === '/chat') return pathname === '/chat' || pathname.startsWith('/chat/')
  return pathname === href
}

// Rail is the desktop workspace navigation: a slim icon column, every
// destination one glance away. Mobile navigation lives in the sheet
// the header trigger opens.
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
  const moreActive = moreNav.some((item) => pathname === item.href)

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
      <Link to="/" aria-label="Timothy home" className="mb-2 flex size-9 items-center justify-center">
        <span className="size-3 rounded-full bg-gradient-to-br from-blue-500 to-violet-600" />
      </Link>
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
      <DropdownMenu>
        <DropdownMenuTrigger className={itemClass(moreActive)} aria-label="More">
          <HugeiconsIcon icon={MoreHorizontalIcon} className="size-5" />
          More
        </DropdownMenuTrigger>
        <DropdownMenuContent side="right" align="start">
          {moreNav.map((item) => (
            <DropdownMenuItem key={item.href} asChild>
              <Link to={item.href}>
                <HugeiconsIcon icon={item.icon} className="size-4" />
                {item.label}
              </Link>
            </DropdownMenuItem>
          ))}
        </DropdownMenuContent>
      </DropdownMenu>

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
  const [sessionsOpen, setSessionsOpen] = useState(true)
  const { pathname } = useLocation()
  const pendingMemories = usePendingMemories()
  const onChat = isActive(pathname, '/chat')

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
      <div className="flex h-dvh w-full">
        <Rail
          pendingMemories={pendingMemories}
          theme={theme}
          onCycleTheme={cycleTheme}
          onToken={openToken}
        />
        {/* Desktop: the sidebar holds sessions and only appears on chat
            routes. Mobile: the same sidebar is the navigation sheet —
            nav links ride above the session list. */}
        <SidebarProvider
          open={onChat && sessionsOpen}
          onOpenChange={setSessionsOpen}
          className="min-h-0 min-w-0 flex-1"
        >
          <Sidebar>
            <SidebarHeader>
              <div className="flex items-center gap-2.5 px-2 py-1.5">
                <span className="size-2.5 rounded-full bg-gradient-to-br from-blue-500 to-violet-600" />
                <span className="text-base font-semibold tracking-tight">Timothy</span>
              </div>
            </SidebarHeader>
            <SidebarContent>
              <SidebarGroup className="md:hidden">
                <SidebarGroupContent>
                  <SidebarMenu>
                    {[...railNav, ...moreNav].map((item) => (
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
            </SidebarContent>
          </Sidebar>

          <SidebarInset className="min-w-0">
            <header
              className={cn('flex h-12 shrink-0 items-center gap-2 px-3', !onChat && 'md:hidden')}
            >
              <SidebarTrigger />
              <span className="text-sm font-semibold tracking-tight md:hidden">Timothy</span>
            </header>
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
                <Route path="/lanes" element={<Lanes />} />
                <Route path="/library" element={<Library />} />
                <Route path="/queues" element={<Queues />} />
                <Route path="/settings" element={<Settings />} />
              </Routes>
            </div>
            <SettingsDialog open={tokenOpen} onClose={() => setTokenOpen(false)} />
          </SidebarInset>
        </SidebarProvider>
      </div>
    </SessionsProvider>
  )
}

export default App
