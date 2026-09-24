import { Ellipsis, Pencil, Play, Plus, Repeat, Trash2 } from 'lucide-react'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { toast } from 'sonner'

import {
  automationsStats,
  deleteAutomation,
  listAgents,
  listAutomations,
  listChannels,
  patchAutomation,
  runAutomationNow,
} from '../api/client'
import type { AdminAgent, Automation, AutomationsStats } from '../api/types'
import { runStatusLabel } from '../components/automations/runs'
import { TemplateGallery } from '../components/automations/TemplateGallery'
import { EChart } from '../components/charts/EChart'
import { sparklineOption } from '../components/charts/options'
import { cssVar } from '../components/charts/theme'
import { ConfirmDialog } from '../components/timothy/confirm-dialog'
import { EmptyState } from '../components/timothy/empty-state'
import { IconButton } from '../components/timothy/icon-button'
import { PageHeader } from '../components/timothy/page-header'
import { PageShell } from '../components/timothy/page-shell'
import { Panel } from '../components/timothy/panel'
import { StatTile } from '../components/timothy/stat-tile'
import { automationRunStatus } from '../components/timothy/status'
import { StatusBadge } from '../components/timothy/status-badge'
import { Badge } from '../components/ui/badge'
import { Button } from '../components/ui/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '../components/ui/dropdown-menu'
import { Switch } from '../components/ui/switch'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '../components/ui/table'
import { describeTrigger } from '../lib/cron'
import { errText } from '../lib/errors'
import { relativeTime } from '../lib/format'

function Sparkline({ rows }: { rows: AutomationsStats['sparkline'] }) {
  const option = useMemo(() => sparklineOption(rows, cssVar('--good'), cssVar('--destructive')), [rows])
  return <EChart option={option} height={40} />
}

// Automations is the dashboard: run stats, templates and every automation.
export function Automations() {
  const navigate = useNavigate()
  const [automations, setAutomations] = useState<Automation[] | null>(null)
  const [stats, setStats] = useState<AutomationsStats | null>(null)
  const [agents, setAgents] = useState<AdminAgent[]>([])
  const [channelNames, setChannelNames] = useState<Record<string, string>>({})
  const [confirmDelete, setConfirmDelete] = useState<Automation | null>(null)

  const refresh = useCallback(() => {
    listAutomations()
      .then(setAutomations)
      .catch((err: unknown) => {
        setAutomations([])
        toast.error('Could not load automations', { description: errText(err) })
      })
    automationsStats().then(setStats, () => setStats(null))
  }, [])
  useEffect(refresh, [refresh])
  useEffect(() => {
    listAgents().then(setAgents, () => setAgents([]))
    listChannels().then(
      (rows) => setChannelNames(Object.fromEntries(rows.map((c) => [c.id, c.name]))),
      () => undefined,
    )
  }, [])

  const toggle = (au: Automation, enabled: boolean) => {
    setAutomations((prev) => prev && prev.map((a) => (a.id === au.id ? { ...a, enabled } : a)))
    patchAutomation(au.id, { enabled }).then(refresh, (err: unknown) => {
      toast.error('Could not update automation', { description: errText(err) })
      refresh()
    })
  }

  const runNow = async (au: Automation) => {
    try {
      await runAutomationNow(au.id)
      toast.success('Run requested')
    } catch (err) {
      toast.error('Could not run automation', { description: errText(err) })
    }
  }

  const remove = async () => {
    if (!confirmDelete) return
    try {
      await deleteAutomation(confirmDelete.id)
      toast.success('Automation deleted')
      refresh()
    } catch (err) {
      toast.error('Could not delete automation', { description: errText(err) })
    }
    setConfirmDelete(null)
  }

  const runs14d = stats ? stats.sparkline.reduce((n, d) => n + d.succeeded + d.failed, 0) : null
  const agentName = (id: string) => agents.find((a) => a.id === id)?.name ?? 'Unknown agent'

  return (
    <PageShell>
      <PageHeader
        title="Automations"
        description="Triggers that start a mission on a cron or when you run them."
        actions={
          <Button onClick={() => navigate('/automations/new')}>
            <Plus aria-hidden />
            New automation
          </Button>
        }
      />

      <div className="space-y-10">
        <div className="grid grid-cols-2 gap-4 sm:grid-cols-3 lg:grid-cols-5">
          <StatTile label="Total" value={stats?.total ?? 'N/A'} />
          <StatTile label="Enabled" value={stats?.enabled ?? 'N/A'} />
          <StatTile label="Succeeded (7d)" value={stats?.succeeded_7d ?? 'N/A'} />
          <StatTile label="Failed (7d)" value={stats?.failed_7d ?? 'N/A'} />
          <StatTile label="Runs, 14 days" value={runs14d ?? 'N/A'}>
            {stats && stats.sparkline.length > 0 && <Sparkline rows={stats.sparkline} />}
          </StatTile>
        </div>

        <TemplateGallery />

        <Panel title="All automations" density="operational">
          {automations !== null && automations.length === 0 ? (
            <EmptyState
              icon={Repeat}
              title="No automations yet"
              description="Create one, start from a template, or choose Make this recurring on a new mission."
            />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Agent</TableHead>
                  <TableHead>Triggers</TableHead>
                  <TableHead>Last run</TableHead>
                  <TableHead>Created</TableHead>
                  <TableHead>
                    <span className="sr-only">Enabled</span>
                  </TableHead>
                  <TableHead>
                    <span className="sr-only">Actions</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {(automations ?? []).map((au) => {
                  const { last_run_status: lastStatus, last_run_at: lastAt } = au.stats
                  return (
                    <TableRow key={au.id} data-automation-id={au.id}>
                      <TableCell>
                        <Link to={`/automations/${au.id}`} className="font-medium hover:underline">
                          {au.name}
                        </Link>
                      </TableCell>
                      <TableCell className="text-muted-foreground">{agentName(au.agent_id)}</TableCell>
                      <TableCell>
                        <div className="flex flex-wrap gap-1.5">
                          {au.triggers.map((t) => (
                            <Badge key={t.id} variant={t.enabled ? 'outline' : 'neutral'} size="sm">
                              {describeTrigger(t, channelNames)}
                            </Badge>
                          ))}
                        </div>
                      </TableCell>
                      <TableCell>
                        {lastStatus ? (
                          <div className="flex items-center gap-2">
                            <StatusBadge status={automationRunStatus(lastStatus)} label={runStatusLabel[lastStatus]} size="sm" />
                            {lastAt && <span className="text-xs text-muted-foreground">{relativeTime(lastAt)}</span>}
                          </div>
                        ) : (
                          <span className="text-xs text-muted-foreground">Never</span>
                        )}
                      </TableCell>
                      <TableCell className="text-xs text-muted-foreground">{relativeTime(au.created_at)}</TableCell>
                      <TableCell>
                        <Switch
                          checked={au.enabled}
                          onCheckedChange={(enabled) => toggle(au, enabled)}
                          aria-label={`${au.name} enabled`}
                        />
                      </TableCell>
                      <TableCell>
                        <DropdownMenu>
                          <DropdownMenuTrigger asChild>
                            <IconButton label={`Actions for ${au.name}`} icon={Ellipsis} size="xs" tooltip={false} />
                          </DropdownMenuTrigger>
                          <DropdownMenuContent align="end">
                            <DropdownMenuItem onClick={() => navigate(`/automations/${au.id}/edit`)}>
                              <Pencil />
                              Edit
                            </DropdownMenuItem>
                            <DropdownMenuItem onClick={() => void runNow(au)} disabled={!au.enabled}>
                              <Play />
                              Run now
                            </DropdownMenuItem>
                            <DropdownMenuItem variant="destructive" onClick={() => setConfirmDelete(au)}>
                              <Trash2 />
                              Delete
                            </DropdownMenuItem>
                          </DropdownMenuContent>
                        </DropdownMenu>
                      </TableCell>
                    </TableRow>
                  )
                })}
              </TableBody>
            </Table>
          )}
        </Panel>
      </div>

      <ConfirmDialog
        open={confirmDelete !== null}
        onOpenChange={(o) => !o && setConfirmDelete(null)}
        title={`Delete ${confirmDelete?.name}?`}
        description="It stops running. Missions it already started keep their history."
        confirmLabel="Delete"
        destructive
        onConfirm={() => void remove()}
      />
    </PageShell>
  )
}
