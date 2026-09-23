import { BookOpen, GitBranch, Inbox, Mail, TestTube, Zap, type LucideIcon } from 'lucide-react'
import { Fragment, useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router'

import { listAutomationTemplates } from '../../api/client'
import type { AutomationRequirement, AutomationTemplate } from '../../api/types'
import { describeTrigger } from '../../lib/cron'
import { SectionHeader } from '../timothy/page-header'
import { Badge } from '../ui/badge'

const icons: Record<string, LucideIcon> = {
  'git-branch': GitBranch,
  mail: Mail,
  'book-open': BookOpen,
  inbox: Inbox,
  'test-tube': TestTube,
}

const settingsPath: Record<string, string> = {
  destination: '/settings/destinations',
  connector: '/settings/connectors',
}

function Requirement({ req }: { req: AutomationRequirement }) {
  const label = `${req.value} ${req.kind}`
  const href = settingsPath[req.kind]
  if (!href) return <span>{label}</span>
  return (
    <Link to={href} className="underline underline-offset-2 hover:text-foreground">
      {label}
    </Link>
  )
}

// TemplateGallery lists starter automations; picking one opens the
// editor prefilled. Renders nothing when the list is empty or fails.
export function TemplateGallery() {
  const navigate = useNavigate()
  const [templates, setTemplates] = useState<AutomationTemplate[]>([])
  useEffect(() => {
    listAutomationTemplates().then(setTemplates, () => setTemplates([]))
  }, [])

  if (templates.length === 0) return null

  return (
    <section>
      <SectionHeader title="Start from a template" description="Nothing is saved until you create it." />
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        {templates.map((t) => {
          const Icon = icons[t.icon] ?? Zap
          return (
            <div key={t.id} className="flex flex-col rounded-md border border-border bg-card">
              <button
                type="button"
                onClick={() => navigate('/automations/new', { state: { template: t } })}
                className="flex flex-1 flex-col items-start gap-2 p-5 text-left outline-none transition-colors duration-100 hover:bg-muted/40 focus-visible:ring-2 focus-visible:ring-ring"
              >
                <span className="flex items-center gap-2">
                  <Icon aria-hidden className="size-4 text-muted-foreground" />
                  <span className="text-sm font-semibold">{t.name}</span>
                </span>
                <span className="text-sm text-muted-foreground">{t.description}</span>
                <span className="flex flex-wrap gap-1.5">
                  {t.triggers.map((tr, i) => (
                    <Badge key={i} variant="outline" size="sm">
                      {describeTrigger(tr)}
                    </Badge>
                  ))}
                </span>
              </button>
              {t.missing.length > 0 && (
                <p className="border-t border-border px-5 py-3 text-xs text-muted-foreground">
                  Needs:{' '}
                  {t.missing.map((req, i) => (
                    <Fragment key={`${req.kind}-${req.value}`}>
                      {i > 0 && ', '}
                      <Requirement req={req} />
                    </Fragment>
                  ))}
                </p>
              )}
            </div>
          )
        })}
      </div>
    </section>
  )
}
