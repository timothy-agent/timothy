import { BookOpen, ListChecks, RotateCcw, Sparkles, type LucideIcon } from 'lucide-react'
import { useLocation, useNavigate } from 'react-router'
import { toast } from 'sonner'
import { errText } from '../lib/errors'
import { useOnboarding } from './context'
import { DOCS_URL } from './docsUrl'
import { requestTourRestart } from './tour/useTour'

export const readmeUrl = 'https://github.com/timothy-agent/timothy#readme'

// tourPageFor maps a path to the page whose tour mounts there, or null.
// List pages carry their tour on the index only, not on detail views.
export function tourPageFor(pathname: string): string | null {
  const p = pathname.replace(/\/+$/, '') || '/'
  const under = (base: string) => p === base || p.startsWith(`${base}/`)
  if (under('/chat')) return 'chat'
  if (p === '/missions/new') return 'mission_new'
  if (p === '/missions') return 'missions'
  if (p === '/automations') return 'automations'
  if (p === '/knowledge') return 'knowledge'
  if (under('/memory')) return 'memory'
  if (p === '/analytics') return 'analytics'
  if (under('/settings')) return 'settings'
  return null
}

export interface HelpAction {
  id: string
  label: string
  icon: LucideIcon
  disabled?: boolean
  // An external link, opened in a new tab instead of run.
  href?: string
  run?: () => void
}

// useHelpActions is the one list behind the help menu and the command
// palette's Help group.
export function useHelpActions(): HelpAction[] {
  const { updateProgress } = useOnboarding()
  const navigate = useNavigate()
  const { pathname } = useLocation()
  const page = tourPageFor(pathname)

  const fail = (title: string) => (err: unknown) => toast.error(title, { description: errText(err) })

  return [
    {
      id: 'checklist',
      label: 'Setup checklist',
      icon: ListChecks,
      run: () => {
        updateProgress({ checklist_dismissed: false }).then(
          () => navigate('/'),
          fail('Could not show the setup checklist'),
        )
      },
    },
    {
      id: 'tour',
      label: "Restart this page's tour",
      icon: RotateCcw,
      disabled: page === null,
      run: () => {
        if (!page) return
        // Clearing the seen version also brings the tour back on a page
        // whose setup gate blocks it right now.
        updateProgress({ tours_seen: { [page]: 0 } }).catch(() => {})
        requestTourRestart(page)
      },
    },
    {
      id: 'welcome',
      label: 'Restart welcome',
      icon: Sparkles,
      run: () => {
        updateProgress({ wizard: 'pending' }).then(
          () => navigate('/welcome'),
          fail('Could not restart the welcome'),
        )
      },
    },
    DOCS_URL
      ? { id: 'docs', label: 'Docs', icon: BookOpen, href: DOCS_URL }
      : { id: 'readme', label: 'README on GitHub', icon: BookOpen, href: readmeUrl },
  ]
}
