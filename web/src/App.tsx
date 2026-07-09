import {
  Bars3Icon,
  ChartBarSquareIcon,
  ChatBubbleLeftRightIcon,
  Cog6ToothIcon,
  ComputerDesktopIcon,
  InboxIcon,
  KeyIcon,
  MoonIcon,
  PhotoIcon,
  RectangleStackIcon,
  SunIcon,
} from '@heroicons/react/20/solid'
import { useEffect, useState } from 'react'
import { Route, Routes, useLocation } from 'react-router'
import { getToken } from './api/client'
import { Navbar, NavbarItem, NavbarSection, NavbarSpacer } from './components/catalyst/navbar'
import {
  Sidebar,
  SidebarBody,
  SidebarHeader,
  SidebarItem,
  SidebarLabel,
  SidebarSection,
  SidebarSpacer,
} from './components/catalyst/sidebar'
import { SidebarLayout } from './components/catalyst/sidebar-layout'
import { SettingsDialog } from './components/SettingsDialog'
import { getTheme, nextTheme, setTheme, type Theme } from './lib/theme'
import { Chat } from './pages/Chat'
import { Dashboard } from './pages/Dashboard'
import { Lanes, Library, Queues, Settings } from './pages/Stubs'

const nav = [
  { label: 'Chat', href: '/', icon: ChatBubbleLeftRightIcon },
  { label: 'Dashboard', href: '/dashboard', icon: ChartBarSquareIcon },
  { label: 'Lanes', href: '/lanes', icon: RectangleStackIcon },
  { label: 'Library', href: '/library', icon: PhotoIcon },
  { label: 'Queues', href: '/queues', icon: InboxIcon },
  { label: 'Settings', href: '/settings', icon: Cog6ToothIcon },
]

const themeIcon = { system: ComputerDesktopIcon, light: SunIcon, dark: MoonIcon }
const themeLabel = { system: 'System theme', light: 'Light theme', dark: 'Dark theme' }

function App() {
  const [tokenOpen, setTokenOpen] = useState(false)
  const [collapsed, setCollapsed] = useState(false)
  const [theme, setThemeState] = useState<Theme>(() => getTheme())
  const { pathname } = useLocation()

  useEffect(() => {
    if (getToken() === '') setTokenOpen(true)
  }, [])

  const cycleTheme = () => {
    const t = nextTheme[theme]
    setTheme(t)
    setThemeState(t)
  }
  const ThemeIcon = themeIcon[theme]

  return (
    <>
      {/* Reopen affordance when the desktop sidebar is collapsed */}
      {collapsed && (
        <button
          onClick={() => setCollapsed(false)}
          aria-label="Open sidebar"
          className="fixed top-4 left-4 z-40 hidden rounded-lg p-2 text-zinc-500 hover:bg-zinc-950/5 lg:block dark:text-zinc-400 dark:hover:bg-white/5"
        >
          <Bars3Icon className="size-5" />
        </button>
      )}

      <SidebarLayout
        collapsed={collapsed}
        navbar={
          <Navbar>
            <NavbarSpacer />
            <NavbarSection>
              <NavbarItem onClick={cycleTheme} aria-label={themeLabel[theme]}>
                <ThemeIcon data-slot="icon" />
              </NavbarItem>
              <NavbarItem onClick={() => setTokenOpen(true)} aria-label="API token">
                <KeyIcon data-slot="icon" />
              </NavbarItem>
            </NavbarSection>
          </Navbar>
        }
        sidebar={
          <Sidebar>
            <SidebarHeader>
              <div className="flex items-center justify-between px-2 py-1">
                <div className="flex items-center gap-2.5">
                  <span className="size-2.5 rounded-full bg-gradient-to-br from-blue-500 to-violet-600" />
                  <span className="text-base font-semibold tracking-tight">Timothy</span>
                </div>
                <button
                  onClick={() => setCollapsed(true)}
                  aria-label="Collapse sidebar"
                  className="hidden rounded-md p-1.5 text-zinc-400 hover:bg-zinc-950/5 hover:text-zinc-600 lg:block dark:hover:bg-white/5 dark:hover:text-zinc-300"
                >
                  <Bars3Icon className="size-4" />
                </button>
              </div>
            </SidebarHeader>
            <SidebarBody>
              <SidebarSection>
                {nav.map((item) => (
                  <SidebarItem key={item.href} href={item.href} current={pathname === item.href}>
                    <item.icon data-slot="icon" />
                    <SidebarLabel>{item.label}</SidebarLabel>
                  </SidebarItem>
                ))}
              </SidebarSection>
              <SidebarSpacer />
              <SidebarSection>
                <SidebarItem onClick={cycleTheme}>
                  <ThemeIcon data-slot="icon" />
                  <SidebarLabel>{themeLabel[theme]}</SidebarLabel>
                </SidebarItem>
                <SidebarItem onClick={() => setTokenOpen(true)}>
                  <KeyIcon data-slot="icon" />
                  <SidebarLabel>API token</SidebarLabel>
                </SidebarItem>
              </SidebarSection>
            </SidebarBody>
          </Sidebar>
        }
      >
        <Routes>
          <Route
            path="/"
            element={
              <div className="mx-auto flex h-[calc(100dvh-6.5rem)] max-w-4xl flex-col lg:h-[calc(100dvh-4.5rem)]">
                <Chat onNeedToken={() => setTokenOpen(true)} />
              </div>
            }
          />
          <Route path="/dashboard" element={<Dashboard />} />
          <Route path="/lanes" element={<Lanes />} />
          <Route path="/library" element={<Library />} />
          <Route path="/queues" element={<Queues />} />
          <Route path="/settings" element={<Settings />} />
        </Routes>
        <SettingsDialog open={tokenOpen} onClose={() => setTokenOpen(false)} />
      </SidebarLayout>
    </>
  )
}

export default App
