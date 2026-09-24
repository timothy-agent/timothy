import { Pencil, Play, Plus, RefreshCw, StickyNote, Trash2 } from 'lucide-react'
import { useCallback, useEffect, useState, type ReactNode } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { toast } from 'sonner'

import {
  deleteAutomationNote,
  getAutomation,
  listAgents,
  listAutomationNotes,
  listAutomationRuns,
  listDestinations,
  patchAutomation,
  putAutomationNote,
  runAutomationNow,
} from '../api/client'
import type { AdminAgent, Automation, AutomationNote, AutomationRun, AutomationTrigger, Destination } from '../api/types'
import { NotesDialog } from '../components/automations/NotesDialog'
import { noteBytes } from '../components/automations/notes'
import { RunHistoryTable } from '../components/automations/RunHistoryTable'
import { isPendingRun } from '../components/automations/runs'
import { draftsFromTriggers, draftsToInput } from '../components/automations/triggerDrafts'
import { ConfirmDialog } from '../components/timothy/confirm-dialog'
import { CopyButton } from '../components/timothy/copy-button'
import { EmptyState } from '../components/timothy/empty-state'
import { IconButton } from '../components/timothy/icon-button'
import { PageHeader } from '../components/timothy/page-header'
import { PageShell } from '../components/timothy/page-shell'
import { Panel } from '../components/timothy/panel'
import { Spinner } from '../components/timothy/spinner'
import { Badge } from '../components/ui/badge'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { Switch } from '../components/ui/switch'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '../components/ui/table'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '../components/ui/tabs'
import { Tooltip, TooltipContent, TooltipTrigger } from '../components/ui/tooltip'
import { describeTrigger, hookPath } from '../lib/cron'
import { errText } from '../lib/errors'
import { euDateTime, humanBytes, money, relativeTime, relativeTimeUntil } from '../lib/format'

// Runs poll this often while any run is queued, starting or running.
export const runPollMs = 15_000
const maxNotes = 10

const concurrencyText: Record<Automation['concurrency'], string> = {
  skip: 'Skip while a run is active',
  queue: 'Queue behind the active run',
  parallel: 'Run in parallel',
}

function Facts({ rows }: { rows: [string, ReactNode][] }) {
  return (
    <dl className="grid grid-cols-[minmax(0,10rem)_1fr] gap-x-4 gap-y-2 text-sm">
      {rows.map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="text-muted-foreground">{k}</dt>
          <dd>{v}</dd>
        </div>
      ))}
    </dl>
  )
}

const onOff = (v: boolean) => (v ? 'On' : 'Off')

const triggerDisabledText: Record<string, string> = {
  auth_failures: 'Disabled after 20 failed signatures',
  rate_limited: 'Disabled: rate limited',
}

function TriggerRow({ trigger: t, onReenable }: { trigger: AutomationTrigger; onReenable: () => void }) {
  const reason = t.state?.disabled_reason
  const lastFired = t.state?.last_fired_at
  const lastDelivery = t.state?.last_delivery_at
  return (
    <li data-trigger-id={t.id} className="space-y-1.5">
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <Badge variant="outline" size="sm">
          {t.kind.replace('_', ' ')}
        </Badge>
        {t.kind !== 'manual' && <span>{describeTrigger(t)}</span>}
        {t.kind === 'cron' && <span className="font-mono text-xs text-muted-foreground">{t.config.expr}</span>}
        {!t.enabled && !reason && <span className="text-xs text-muted-foreground">off</span>}
        {reason && (
          <>
            <Badge variant="destructive" size="sm">
              {triggerDisabledText[reason] ?? `Disabled: ${reason.replace('_', ' ')}`}
            </Badge>
            <Button size="xs" variant="outline" onClick={onReenable}>
              Re-enable
            </Button>
          </>
        )}
      </div>
      {t.kind === 'webhook' && (
        <div className="flex items-center gap-1.5">
          <code className="font-mono text-xs text-muted-foreground">POST {hookPath(t.id)}</code>
          <CopyButton value={hookPath(t.id)} label="Copy hook path" />
        </div>
      )}
      {(lastFired || lastDelivery) && (
        <p className="text-xs text-muted-foreground">
          {[lastFired && `Last fired ${relativeTime(lastFired)}`, lastDelivery && `Last delivery ${relativeTime(lastDelivery)}`]
            .filter(Boolean)
            .join(' · ')}
        </p>
      )}
    </li>
  )
}

// AutomationDetail shows one automation: settings, run history and notes.
export function AutomationDetail() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const [automation, setAutomation] = useState<Automation | null>(null)
  const [loading, setLoading] = useState(true)
  const [runs, setRuns] = useState<AutomationRun[]>([])
  const [notes, setNotes] = useState<AutomationNote[]>([])
  const [agents, setAgents] = useState<AdminAgent[]>([])
  const [destinations, setDestinations] = useState<Destination[]>([])
  const [renaming, setRenaming] = useState(false)
  const [name, setName] = useState('')
  const [noteDialog, setNoteDialog] = useState<{ note: AutomationNote | null } | null>(null)
  const [confirmNote, setConfirmNote] = useState<AutomationNote | null>(null)

  const loadAutomation = useCallback(() => {
    if (!id) return
    getAutomation(id)
      .then(setAutomation, () => setAutomation(null))
      .finally(() => setLoading(false))
  }, [id])
  const loadRuns = useCallback(() => {
    if (id) listAutomationRuns(id).then(setRuns, () => undefined)
  }, [id])
  const loadNotes = useCallback(() => {
    if (id) listAutomationNotes(id).then(setNotes, () => undefined)
  }, [id])

  useEffect(() => {
    loadAutomation()
    loadRuns()
    loadNotes()
  }, [loadAutomation, loadRuns, loadNotes])
  useEffect(() => {
    listAgents().then(setAgents, () => undefined)
    listDestinations().then(setDestinations, () => undefined)
  }, [])

  const pending = runs.some(isPendingRun)
  useEffect(() => {
    if (!pending) return
    const t = setInterval(loadRuns, runPollMs)
    return () => clearInterval(t)
  }, [pending, loadRuns])

  const commitRename = async () => {
    const trimmed = name.trim()
    setRenaming(false)
    if (!automation || trimmed === '' || trimmed === automation.name) return
    try {
      setAutomation(await patchAutomation(automation.id, { name: trimmed }))
      toast.success('Automation renamed')
    } catch (err) {
      toast.error('Could not rename automation', { description: errText(err) })
    }
  }

  const setEnabled = async (enabled: boolean) => {
    if (!automation) return
    try {
      setAutomation(await patchAutomation(automation.id, { enabled }))
    } catch (err) {
      toast.error('Could not update automation', { description: errText(err) })
    }
  }

  // reenableTrigger sends the full trigger set with one trigger back on;
  // the server clears its disabled_reason.
  const reenableTrigger = async (triggerId: string) => {
    if (!automation) return
    const triggers = draftsToInput(draftsFromTriggers(automation.triggers)).map((t) =>
      t.id === triggerId ? { ...t, enabled: true } : t,
    )
    try {
      setAutomation(await patchAutomation(automation.id, { triggers }))
      toast.success('Trigger re-enabled')
    } catch (err) {
      toast.error('Could not re-enable trigger', { description: errText(err) })
    }
  }

  const runNow = async () => {
    if (!automation) return
    try {
      await runAutomationNow(automation.id)
      toast.success('Run requested')
      loadRuns()
    } catch (err) {
      toast.error('Could not run automation', { description: errText(err) })
    }
  }

  const saveNote = async (noteName: string, content: string) => {
    if (!automation) return false
    try {
      await putAutomationNote(automation.id, noteName, content)
      toast.success('Note saved')
      loadNotes()
      return true
    } catch (err) {
      toast.error('Could not save note', { description: errText(err) })
      return false
    }
  }

  const removeNote = async () => {
    if (!automation || !confirmNote) return
    try {
      await deleteAutomationNote(automation.id, confirmNote.name)
      toast.success('Note deleted')
      loadNotes()
    } catch (err) {
      toast.error('Could not delete note', { description: errText(err) })
    }
    setConfirmNote(null)
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

  const mission = automation.action.mission
  const agentName = agents.find((a) => a.id === automation.agent_id)?.name
  const destinationNames = (mission.destination_ids ?? []).map((did) => destinations.find((d) => d.id === did)?.name ?? did)
  const nextRun = automation.stats.next_run_at
  const statusBadge = (
    <Badge variant={automation.enabled ? 'good' : 'neutral'} size="sm" tabIndex={automation.disabled_reason ? 0 : undefined}>
      {automation.enabled ? 'enabled' : 'disabled'}
    </Badge>
  )

  const updatedBy = (n: AutomationNote) => {
    if (!n.updated_by_run_id) return 'You'
    const missionId = runs.find((r) => r.id === n.updated_by_run_id)?.mission_id
    if (!missionId) return 'A run'
    return (
      <Link to={`/missions/${missionId}`} className="underline underline-offset-2 hover:text-foreground">
        Run mission
      </Link>
    )
  }

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
              <IconButton
                label="Rename automation"
                icon={Pencil}
                variant="ghost"
                size="sm"
                onClick={() => {
                  setName(automation.name)
                  setRenaming(true)
                }}
              />
            </div>
          )
        }
        meta={
          <>
            {automation.disabled_reason ? (
              <Tooltip>
                <TooltipTrigger asChild>{statusBadge}</TooltipTrigger>
                <TooltipContent>{automation.disabled_reason}</TooltipContent>
              </Tooltip>
            ) : (
              statusBadge
            )}
            {agentName && (
              <Badge variant="outline" size="sm">
                {agentName}
              </Badge>
            )}
          </>
        }
        description={automation.description || undefined}
        actions={
          <>
            <div className="flex items-center gap-2">
              <Switch id="automation-enabled" checked={automation.enabled} onCheckedChange={(v) => void setEnabled(v)} />
              <label htmlFor="automation-enabled" className="text-sm">
                Enabled
              </label>
            </div>
            <Button variant="outline" onClick={() => void runNow()} disabled={!automation.enabled}>
              <Play aria-hidden />
              Run now
            </Button>
            <Button variant="outline" onClick={() => navigate(`/automations/${automation.id}/edit`)}>
              Edit
            </Button>
          </>
        }
      />

      <Tabs defaultValue="settings">
        <TabsList>
          <TabsTrigger value="settings">Settings</TabsTrigger>
          <TabsTrigger value="runs">Run history</TabsTrigger>
          <TabsTrigger value="notes">Notes</TabsTrigger>
        </TabsList>

        <TabsContent value="settings" className="mt-8 space-y-6">
          <Panel title="Triggers" description={nextRun ? `Next run ${relativeTimeUntil(nextRun)}` : undefined}>
            <ul className="space-y-3">
              {automation.triggers.map((t) => (
                <TriggerRow key={t.id} trigger={t} onReenable={() => void reenableTrigger(t.id)} />
              ))}
            </ul>
          </Panel>

          <Panel title="Action">
            <p className="text-prose whitespace-pre-wrap">{mission.goal}</p>
            <div className="mt-5">
              <Facts
                rows={[
                  ['Kind', mission.kind === 'coding' ? 'Coding' : mission.light ? 'General, light' : 'General'],
                  ['Route', mission.route || 'Default'],
                  ['Budget per run', mission.budget_amount ? money(mission.budget_amount, mission.budget_currency || 'USD') : 'No limit'],
                  ['Destinations', destinationNames.length > 0 ? destinationNames.join(', ') : 'None'],
                  ['Attachments', String(mission.attachments?.length ?? 0)],
                ]}
              />
            </div>
          </Panel>

          <Panel title="Limits">
            <Facts
              rows={[
                ['Concurrency', concurrencyText[automation.concurrency]],
                ['Max concurrent runs', String(automation.max_concurrent)],
                ['Max runs per hour', String(automation.max_runs_per_hour)],
                ['Pass result forward', onOff(automation.continuity)],
                ['Notes', onOff(automation.notes_enabled)],
                ['Expires', automation.expires_at ? euDateTime(automation.expires_at) : 'Never'],
                ['Failures in a row', String(automation.consecutive_failures)],
              ]}
            />
          </Panel>
        </TabsContent>

        <TabsContent value="runs" className="mt-8">
          <Panel
            title="Run history"
            density="operational"
            actions={<IconButton label="Refresh runs" icon={RefreshCw} size="sm" onClick={loadRuns} />}
          >
            {runs.length > 0 ? (
              <RunHistoryTable runs={runs} triggers={automation.triggers} />
            ) : (
              <EmptyState title="No runs yet" density="operational" />
            )}
          </Panel>
        </TabsContent>

        <TabsContent value="notes" className="mt-8 space-y-4">
          {!automation.notes_enabled && (
            <p className="text-sm text-muted-foreground">Notes are off, so runs do not read or update them.</p>
          )}
          <Panel
            title="Notes"
            description={`${notes.length} of ${maxNotes}`}
            density="operational"
            actions={
              <Button size="sm" variant="outline" disabled={notes.length >= maxNotes} onClick={() => setNoteDialog({ note: null })}>
                <Plus aria-hidden />
                Add note
              </Button>
            }
          >
            {notes.length > 0 ? (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Name</TableHead>
                    <TableHead numeric>Size</TableHead>
                    <TableHead>Updated</TableHead>
                    <TableHead>Updated by</TableHead>
                    <TableHead>
                      <span className="sr-only">Actions</span>
                    </TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {notes.map((n) => (
                    <TableRow key={n.name} data-note={n.name}>
                      <TableCell className="font-mono text-xs">{n.name}</TableCell>
                      <TableCell numeric className="text-xs text-muted-foreground">
                        {humanBytes(noteBytes(n.content))}
                      </TableCell>
                      <TableCell className="text-xs text-muted-foreground">{relativeTime(n.updated_at)}</TableCell>
                      <TableCell className="text-xs text-muted-foreground">{updatedBy(n)}</TableCell>
                      <TableCell>
                        <div className="flex justify-end gap-1">
                          <IconButton label={`Edit ${n.name}`} icon={Pencil} size="xs" onClick={() => setNoteDialog({ note: n })} />
                          <IconButton label={`Delete ${n.name}`} icon={Trash2} size="xs" onClick={() => setConfirmNote(n)} />
                        </div>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            ) : (
              <EmptyState icon={StickyNote} title="No notes yet" density="operational" />
            )}
          </Panel>
        </TabsContent>
      </Tabs>

      <NotesDialog
        open={noteDialog !== null}
        onOpenChange={(o) => !o && setNoteDialog(null)}
        note={noteDialog?.note}
        onSave={saveNote}
      />
      <ConfirmDialog
        open={confirmNote !== null}
        onOpenChange={(o) => !o && setConfirmNote(null)}
        title={`Delete ${confirmNote?.name}?`}
        description="Runs lose what this note held."
        confirmLabel="Delete"
        destructive
        onConfirm={() => void removeNote()}
      />
    </PageShell>
  )
}
