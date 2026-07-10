import {
  Analytics01Icon,
  BubbleChatIcon,
  ComputerIcon,
  Image01Icon,
  InboxIcon,
  Key01Icon,
  Layers01Icon,
  Moon02Icon,
  Settings02Icon,
  Sun03Icon,
} from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useCallback, useEffect, useState } from 'react'
import { Link, Route, Routes, useLocation } from 'react-router'
import { getToken } from './api/client'
import { SessionList } from './components/SessionList'
import { SessionsProvider } from './components/SessionsProvider'
import { SettingsDialog } from './components/SettingsDialog'
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarHeader,
  SidebarInset,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarProvider,
  SidebarTrigger,
} from './components/ui/sidebar'
import { getTheme, nextTheme, setTheme, type Theme } from './lib/theme'
import { Chat } from './pages/Chat'
import { Dashboard } from './pages/Dashboard'
import { Lanes, Library, Queues, Settings } from './pages/Stubs'

const nav = [
  { label: 'Chat', href: '/', icon: BubbleChatIcon },
  { label: 'Dashboard', href: '/dashboard', icon: Analytics01Icon },
  { label: 'Lanes', href: '/lanes', icon: Layers01Icon },
  { label: 'Library', href: '/library', icon: Image01Icon },
  { label: 'Queues', href: '/queues', icon: InboxIcon },
  { label: 'Settings', href: '/settings', icon: Settings02Icon },
]

const themeIcon = { system: ComputerIcon, light: Sun03Icon, dark: Moon02Icon }
const themeLabel = { system: 'System theme', light: 'Light theme', dark: 'Dark theme' }

function App() {
  const [tokenOpen, setTokenOpen] = useState(false)
  const [theme, setThemeState] = useState<Theme>(() => getTheme())
  const { pathname } = useLocation()

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
      <SidebarProvider>
        <Sidebar>
          <SidebarHeader>
            <div className="flex items-center gap-2.5 px-2 py-1.5">
              <span className="size-2.5 rounded-full bg-gradient-to-br from-blue-500 to-violet-600" />
              <span className="text-base font-semibold tracking-tight">Timothy</span>
            </div>
          </SidebarHeader>
          <SidebarContent>
            <SidebarGroup>
              <SidebarGroupContent>
                <SidebarMenu>
                  {nav.map((item) => (
                    <SidebarMenuItem key={item.href}>
                      <SidebarMenuButton
                        asChild
                        isActive={
                          item.href === '/'
                            ? pathname === '/' || pathname.startsWith('/sessions')
                            : pathname === item.href
                        }
                      >
                        <Link to={item.href}>
                          <HugeiconsIcon icon={item.icon} />
                          <span>{item.label}</span>
                        </Link>
                      </SidebarMenuButton>
                    </SidebarMenuItem>
                  ))}
                </SidebarMenu>
              </SidebarGroupContent>
            </SidebarGroup>
            <SessionList />
          </SidebarContent>
          <SidebarFooter>
            <SidebarMenu>
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
          </SidebarFooter>
        </Sidebar>

        <SidebarInset>
          <header className="flex h-12 shrink-0 items-center gap-2 px-3">
            <SidebarTrigger />
          </header>
          <div className="flex-1 overflow-hidden px-4">
            <Routes>
              {/* One route pattern serves new chats and resumes: switching
                  between them must re-render, not remount, so an in-flight
                  stream survives adopting its new session URL. */}
              <Route
                path="/sessions?/:id?"
                element={
                  <div className="mx-auto flex h-[calc(100dvh-3.5rem)] max-w-4xl flex-col">
                    <Chat onNeedToken={openToken} />
                  </div>
                }
              />
              <Route path="/dashboard" element={<Dashboard />} />
              <Route path="/lanes" element={<Lanes />} />
              <Route path="/library" element={<Library />} />
              <Route path="/queues" element={<Queues />} />
              <Route path="/settings" element={<Settings />} />
            </Routes>
          </div>
          <SettingsDialog open={tokenOpen} onClose={() => setTokenOpen(false)} />
        </SidebarInset>
      </SidebarProvider>
    </SessionsProvider>
  )
}

export default App
