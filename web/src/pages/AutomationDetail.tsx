import { Pencil, Play } from 'lucide-react'
import { useCallback, useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { toast } from 'sonner'
import { getAutomation, listAutomationRuns, listDestinations, patchAutomation, runAutomationNow } from '../api/client'
import type { Automation, AutomationRun, Destination } from '../api/types'
import { DestinationKindIcon } from '../components/destinations/DestinationKindIcon'
import { errText } from '../lib/errors'
import { EmptyState } from '../components/timothy/empty-state'
import { IconButton } from '../components/timothy/icon-button'
import { PageHeader } from '../components/timothy/page-header'
import { PageShell } from '../components/timothy/page-shell'
import { Spinner } from '../components/timothy/spinner'
import { automationRunStatus } from '../components/timothy/status'
import { StatusBadge } from '../components/timothy/status-badge'
import { Badge } from '../components/ui/badge'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { cronExpr, describeCron } from '../lib/cron'
import { relativeTime, relativeTimeUntil } from '../lib/format'

// AutomationDetail shows one automation's summary plus its run history.
export function AutomationDetail() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const [automation, setAutomation] = useState<Automation | null>(null)
  const [loading, setLoading] = useState(true)
  const [runs, setRuns] = useState<AutomationRun[]>([])
  const [renaming, setRenaming] = useState(false)
  const [name, setName] = useState('')
  // Destinations fetched once per page, just to resolve
  // destination_ids into display names for the badges below.
  const [destinations, setDestinations] = useState<Destination[]>([])
  useEffect(() => {
    listDestinations()
      .then(setDestinations)
      .catch(() => {
        // Non-fatal: badges just don't render if this fails.
      })
  }, [])

  const refresh = useCallback(() => {
    if (!id) return
    getAutomation(id).then(
      (a) => {
        setAutomation(a)
        setLoading(false)
      },
      () => {
        setAutomation(null)
        setLoading(false)
      },
    )
    listAutomationRuns(id).then(setRuns, () => undefined)
  }, [id])

  useEffect(refresh, [refresh])

  const startRename = () => {
    if (!automation) return
    setName(automation.name)
    setRenaming(true)
  }

  const commitRename = async () => {
    const trimmed = name.trim()
    setRenaming(false)
    if (!automation || trimmed === '' || trimmed === automation.name) return
    try {
      await patchAutomation(automation.id, { name: trimmed })
      toast.success('Automation renamed')
      refresh()
    } catch (err) {
      toast.error('Could not rename automation', { description: errText(err) })
    }
  }

  const runNow = async () => {
    if (!automation) return
    try {
      await runAutomationNow(automation.id)
      toast.success('Run requested')
      refresh()
    } catch (err) {
      toast.error('Could not run automation', { description: errText(err) })
    }
  }

  if (!id) return null

  if (loading) {
    return (
      <PageShell>
        <Spinner label="Loading" />
      </PageShell>
    )
  }

  if (!automation) {
    return (
      <PageShell>
        <p className="text-sm text-muted-foreground">
          Automation not found.{' '}
          <Link to="/automations" className="underline underline-offset-2 hover:text-foreground">
            Back to automations
          </Link>
        </p>
      </PageShell>
    )
  }

  const destinationIds = automation.action.mission.destination_ids ?? []
  const cron = cronExpr(automation)
  const { next_run_at: nextRun, last_run_at: lastRun } = automation.stats

  return (
    <PageShell>
      <PageHeader
        breadcrumbs={[{ label: 'Automations', href: '/automations' }, { label: automation.name }]}
        title={automation.name}
        titleNode={
          renaming ? (
            <Input
              aria-label="Automation name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              onBlur={() => void commitRename()}
              onKeyDown={(e) => {
                if (e.key === 'Enter') e.currentTarget.blur()
                if (e.key === 'Escape') setRenaming(false)
              }}
              autoFocus
              className="h-8 max-w-sm text-title font-semibold"
            />
          ) : (
            <div className="flex items-center gap-1.5">
              <h1 className="truncate text-title font-semibold text-foreground">{automation.name}</h1>
              <IconButton label="Rename automation" icon={Pencil} variant="ghost" size="sm" onClick={startRename} />
            </div>
          )
        }
        meta={
          <>
            <Badge variant={automation.enabled ? 'good' : 'neutral'} size="sm">
              {automation.enabled ? 'enabled' : 'disabled'}
            </Badge>
            {destinationIds.map((did) => {
              const d = destinations.find((d) => d.id === did)
              return (
                <Badge key={did} variant="outline" size="sm">
                  {d && <DestinationKindIcon kind={d.kind} />}
                  {d?.name ?? did}
                </Badge>
              )
            })}
          </>
        }
        description={automation.action.mission.goal}
        actions={
          <>
            <Button variant="outline" onClick={() => void runNow()}>
              <Play aria-hidden />
              Run now
            </Button>
            <Button variant="outline" onClick={() => navigate(`/automations/${id}/edit`)}>
              Edit
            </Button>
          </>
        }
      >
        <div className="flex flex-wrap items-center gap-x-1.5 gap-y-0.5 text-xs text-muted-foreground">
          {cron && <span className="whitespace-nowrap">{describeCron(cron)}</span>}
          {nextRun && (
            <>
              {cron && <span aria-hidden>&middot;</span>}
              <span className="whitespace-nowrap">Next run {relativeTimeUntil(nextRun)}</span>
            </>
          )}
          {lastRun && (
            <>
              {(cron || nextRun) && <span aria-hidden>&middot;</span>}
              <span className="whitespace-nowrap">Last run {relativeTime(lastRun)}</span>
            </>
          )}
        </div>
      </PageHeader>

      {runs.length > 0 ? (
        <ul aria-label="Runs" className="mt-8 divide-y divide-border rounded-md border border-border">
          {runs.map((r) => (
            <li key={r.id} className="flex flex-wrap items-center gap-x-3 gap-y-1 px-4 py-3 text-sm">
              <StatusBadge status={automationRunStatus(r.status)} label={r.status} size="sm" />
              <span className="whitespace-nowrap text-muted-foreground">{relativeTime(r.created_at)}</span>
              {r.skip_reason && <span className="text-muted-foreground">{r.skip_reason}</span>}
              {r.mission_id && (
                <Link to={`/missions/${r.mission_id}`} className="ml-auto underline underline-offset-2 hover:text-foreground">
                  View mission
                </Link>
              )}
            </li>
          ))}
        </ul>
      ) : (
        <div className="mt-8 rounded-md border border-dashed border-border">
          <EmptyState title="No runs yet." />
        </div>
      )}
    </PageShell>
  )
}
