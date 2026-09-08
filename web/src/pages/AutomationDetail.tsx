import { Pencil } from 'lucide-react'
import { useCallback, useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { toast } from 'sonner'
import { listDestinations, listMissions, listSchedules, patchSchedule } from '../api/client'
import type { Destination, Mission, Schedule } from '../api/types'
import { MissionCard } from '../components/missions/MissionCard'
import { DestinationKindIcon } from '../components/destinations/DestinationKindIcon'
import { errText } from '../lib/errors'
import { slugify } from '../lib/slugify'
import { EmptyState } from '../components/timothy/empty-state'
import { IconButton } from '../components/timothy/icon-button'
import { PageHeader } from '../components/timothy/page-header'
import { PageShell } from '../components/timothy/page-shell'
import { Spinner } from '../components/timothy/spinner'
import { Badge } from '../components/ui/badge'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { describeCron } from '../lib/schedules'
import { relativeTime, relativeTimeUntil } from '../lib/format'

// AutomationDetail shows one schedule's summary plus the missions it
// has fired — no GET-by-id for schedules, same as EditSchedule, so the
// list (small) is searched for the one being viewed.
export function AutomationDetail() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const [schedule, setSchedule] = useState<Schedule | null>(null)
  const [loading, setLoading] = useState(true)
  const [missions, setMissions] = useState<Mission[]>([])
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
    listSchedules().then(
      (rows) => {
        setSchedule(rows.find((s) => s.id === id) ?? null)
        setLoading(false)
      },
      () => setLoading(false),
    )
    listMissions({ scheduleId: id }).then(setMissions, () => undefined)
  }, [id])

  useEffect(refresh, [refresh])

  const startRename = () => {
    if (!schedule) return
    setName(schedule.name)
    setRenaming(true)
  }

  const commitRename = async () => {
    const slug = slugify(name)
    setRenaming(false)
    if (!schedule || slug === '' || slug === schedule.name) return
    try {
      await patchSchedule(schedule.id, { name: slug })
      toast.success('Automation renamed')
      refresh()
    } catch (err) {
      toast.error('Could not rename automation', { description: errText(err) })
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

  if (!schedule) {
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

  const destinationIds = schedule.mission_template.destination_ids ?? []

  return (
    <PageShell>
      <PageHeader
        breadcrumbs={[{ label: 'Automations', href: '/automations' }, { label: schedule.name }]}
        title={schedule.name}
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
              <h1 className="truncate text-title font-semibold text-foreground">{schedule.name}</h1>
              <IconButton label="Rename automation" icon={Pencil} variant="ghost" size="sm" onClick={startRename} />
            </div>
          )
        }
        meta={
          <>
            <Badge variant={schedule.enabled ? 'good' : 'neutral'} size="sm">
              {schedule.enabled ? 'enabled' : 'disabled'}
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
        description={schedule.mission_template.goal}
        actions={
          <Button variant="outline" onClick={() => navigate(`/automations/${id}/edit`)}>
            Edit
          </Button>
        }
      >
        <div className="flex flex-wrap items-center gap-x-1.5 gap-y-0.5 text-xs text-muted-foreground">
          <span className="whitespace-nowrap">{describeCron(schedule.cron)}</span>
          {schedule.next_run && (
            <>
              <span aria-hidden>&middot;</span>
              <span className="whitespace-nowrap">Next run {relativeTimeUntil(schedule.next_run)}</span>
            </>
          )}
          {schedule.last_run && (
            <>
              <span aria-hidden>&middot;</span>
              <span className="whitespace-nowrap">Last run {relativeTime(schedule.last_run)}</span>
            </>
          )}
        </div>
      </PageHeader>

      <div className="mt-8 grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        {missions.map((m) => (
          <MissionCard key={m.id} mission={m} />
        ))}
        {missions.length === 0 && (
          <div className="col-span-full rounded-md border border-dashed border-border">
            <EmptyState title="No missions fired yet." />
          </div>
        )}
      </div>
    </PageShell>
  )
}
