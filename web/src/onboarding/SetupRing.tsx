import { Link } from 'react-router'
import { SidebarMenuButton, SidebarMenuItem } from '../components/ui/sidebar'
import { buildChecklist, checklistProgress } from './checklist'
import { useOnboarding } from './context'

const radius = 7
const circumference = 2 * Math.PI * radius

// SetupRing is the sidebar's setup progress link back to the checklist.
export function SetupRing() {
  const { readiness, progress } = useOnboarding()
  if (!readiness || progress.checklist_dismissed === true) return null
  const { done, total, percent } = checklistProgress(buildChecklist(readiness, progress))
  if (done === total) return null
  const label = `Setup ${done}/${total}`

  return (
    <SidebarMenuItem>
      <SidebarMenuButton asChild tooltip={label}>
        <Link to="/">
          <svg viewBox="0 0 18 18" className="size-4 -rotate-90" aria-hidden="true">
            <circle cx="9" cy="9" r={radius} fill="none" strokeWidth="2.5" className="stroke-muted" />
            <circle
              cx="9"
              cy="9"
              r={radius}
              fill="none"
              strokeWidth="2.5"
              strokeLinecap="round"
              strokeDasharray={`${(percent / 100) * circumference} ${circumference}`}
              className="stroke-brand"
            />
          </svg>
          <span>{label}</span>
        </Link>
      </SidebarMenuButton>
    </SidebarMenuItem>
  )
}
