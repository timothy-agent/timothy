import { ChevronRight } from 'lucide-react'
import { useEffect, useState } from 'react'
import { Link, useLocation, useNavigate, useParams } from 'react-router'
import { toast } from 'sonner'

import { createAutomation, getAutomation, listConnectors, listDestinations, patchAutomation } from '../api/client'
import type { AdminConnector, Automation, AutomationAction, AutomationTemplate, Destination, MissionTemplate } from '../api/types'
import { useAgents, useRoutes } from '../components/AgentPicker'
import {
  draftsFromTriggers,
  draftsToInput,
  goalHint,
  newTriggerDraft,
  triggerError,
  type TriggerDraft,
} from '../components/automations/triggerDrafts'
import { TriggerList } from '../components/automations/TriggerList'
import { type PendingAttachment } from '../components/Composer'
import { MissionActionFields } from '../components/missions/MissionActionFields'
import { normalizeTemplate } from '../components/missions/missionTemplate'
import { Field, FieldGroup, Form, FormActions } from '../components/timothy/field'
import { PageHeader } from '../components/timothy/page-header'
import { PageShell } from '../components/timothy/page-shell'
import { SegmentedControl } from '../components/timothy/segmented-control'
import { Spinner } from '../components/timothy/spinner'
import { Button } from '../components/ui/button'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '../components/ui/collapsible'
import { Input } from '../components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../components/ui/select'
import { Switch } from '../components/ui/switch'
import { Textarea } from '../components/ui/textarea'
import { isoToLocalInput, localInputToIso } from '../lib/datetimeLocal'
import { errText } from '../lib/errors'
import { cn } from '../lib/utils'

// Router state the editor seeds from: a gallery template, or a mission
// form's "Make this recurring" prefill.
export interface EditorLocationState {
  template?: AutomationTemplate
  prefill?: { action: AutomationAction; agent_id?: string }
}

type Concurrency = Automation['concurrency']

interface Draft {
  name: string
  description: string
  agentId: string
  enabled: boolean
  expiresLocal: string
  triggers: TriggerDraft[]
  mission: MissionTemplate
  attachments: PendingAttachment[]
  concurrency: Concurrency
  maxConcurrent: number
  maxRunsPerHour: number
  continuity: boolean
  notesEnabled: boolean
}

const limitDefaults = { concurrency: 'skip' as Concurrency, maxConcurrent: 1, maxRunsPerHour: 6, continuity: true, notesEnabled: true }

const blankMission: MissionTemplate = { goal: '', kind: 'general', auto_approve_tools: true }

const toPending = (t: MissionTemplate): PendingAttachment[] =>
  (t.attachments ?? []).map((a) => ({ id: a.id, name: a.name, mime: a.mime ?? '', previewUrl: '' }))

function blankDraft(): Draft {
  return {
    name: '',
    description: '',
    agentId: '',
    enabled: true,
    expiresLocal: '',
    triggers: [newTriggerDraft()],
    mission: blankMission,
    attachments: [],
    ...limitDefaults,
  }
}

// seedDraft builds the starting form from an automation (edit) or the
// router state (create).
function seedDraft(automation: Automation | null, state: EditorLocationState): Draft {
  if (automation) {
    const mission = automation.action.mission
    return {
      name: automation.name,
      description: automation.description,
      agentId: automation.agent_id,
      enabled: automation.enabled,
      expiresLocal: isoToLocalInput(automation.expires_at),
      triggers: draftsFromTriggers(automation.triggers),
      mission,
      attachments: toPending(mission),
      concurrency: automation.concurrency,
      maxConcurrent: automation.max_concurrent,
      maxRunsPerHour: automation.max_runs_per_hour,
      continuity: automation.continuity,
      notesEnabled: automation.notes_enabled,
    }
  }
  const base = blankDraft()
  if (state.template) {
    const t = state.template
    const mission = t.action.mission ?? blankMission
    return {
      ...base,
      name: t.name,
      description: t.description,
      triggers: t.triggers.length > 0 ? draftsFromTriggers(t.triggers) : base.triggers,
      mission,
      attachments: toPending(mission),
      concurrency: t.concurrency ?? base.concurrency,
      continuity: t.continuity ?? base.continuity,
      notesEnabled: t.notes_enabled ?? base.notesEnabled,
    }
  }
  if (state.prefill) {
    const mission = state.prefill.action.mission ?? blankMission
    return { ...base, agentId: state.prefill.agent_id ?? '', mission, attachments: toPending(mission) }
  }
  return base
}

// triggerIndex reads the trigger index from a server message such as
// "triggers[1]: invalid cron expression: ...".
function triggerIndex(message: string): number {
  const m = message.match(/triggers\[(\d+)\]/)
  return m ? Number(m[1]) : -1
}

const concurrencyHelp: Record<Concurrency, string> = {
  skip: 'A trigger that fires while a run is active is skipped.',
  queue: 'A trigger that fires while a run is active waits for it to finish.',
  parallel: 'Runs start side by side, up to the limit below.',
}

// AutomationEditor creates or edits one automation.
export function AutomationEditor({ mode }: { mode: 'create' | 'edit' }) {
  const { id } = useParams<{ id: string }>()
  const state = (useLocation().state ?? {}) as EditorLocationState
  const [automation, setAutomation] = useState<Automation | null>(null)
  const [loading, setLoading] = useState(mode === 'edit')

  useEffect(() => {
    if (mode !== 'edit' || !id) return
    getAutomation(id)
      .then(setAutomation, () => setAutomation(null))
      .finally(() => setLoading(false))
  }, [mode, id])

  if (loading) {
    return (
      <PageShell width="form">
        <Spinner label="Loading" />
      </PageShell>
    )
  }

  if (mode === 'edit' && !automation) {
    return (
      <PageShell width="form">
        <p className="text-sm text-muted-foreground">
          Automation not found.{' '}
          <Link to="/automations" className="underline underline-offset-2 hover:text-foreground">
            Back to automations
          </Link>
        </p>
      </PageShell>
    )
  }

  return <EditorForm automation={mode === 'edit' ? automation : null} initial={seedDraft(automation, state)} />
}

function EditorForm({ automation, initial }: { automation: Automation | null; initial: Draft }) {
  const navigate = useNavigate()
  const agents = useAgents()
  const routes = useRoutes()
  const [destinations, setDestinations] = useState<Destination[] | null>(null)
  const [connectors, setConnectors] = useState<AdminConnector[] | null>(null)
  const [draft, setDraft] = useState(initial)
  const [showAdvanced, setShowAdvanced] = useState(
    initial.concurrency !== limitDefaults.concurrency ||
      initial.maxRunsPerHour !== limitDefaults.maxRunsPerHour ||
      initial.continuity !== limitDefaults.continuity ||
      initial.notesEnabled !== limitDefaults.notesEnabled,
  )
  const [submitted, setSubmitted] = useState(false)
  const [nameError, setNameError] = useState<string>()
  const [triggerErrors, setTriggerErrors] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)
  const editing = automation !== null

  useEffect(() => {
    listDestinations().then(setDestinations, () => setDestinations([]))
    listConnectors().then(setConnectors, () => setConnectors([]))
  }, [])

  // An untouched agent falls back to the default agent.
  const agentId = draft.agentId || (agents.find((a) => a.is_default) ?? agents[0])?.id || ''

  const set = (patch: Partial<Draft>) => setDraft((d) => ({ ...d, ...patch }))
  const hasCron = draft.triggers.some((t) => t.kind === 'cron')
  const rateOk = Number.isInteger(draft.maxRunsPerHour) && draft.maxRunsPerHour >= 1 && draft.maxRunsPerHour <= 60
  const errors = {
    name: nameError ?? (submitted && draft.name.trim() === '' ? 'Enter a name.' : undefined),
    goal: submitted && draft.mission.goal.trim() === '' ? 'Enter a goal.' : undefined,
    agent: submitted && !agentId ? 'Pick an agent.' : undefined,
    rate: rateOk ? undefined : 'Use a whole number from 1 to 60.',
  }

  const submit = async () => {
    setSubmitted(true)
    const invalid =
      draft.name.trim() === '' ||
      draft.mission.goal.trim() === '' ||
      !agentId ||
      !rateOk ||
      draft.triggers.some((t) => triggerError(t)) ||
      draft.attachments.some((a) => a.uploading)
    if (invalid) return

    const mission = normalizeTemplate({
      ...draft.mission,
      attachments: draft.attachments.map((a) => ({ id: a.id, name: a.name ?? '' })),
    })
    const common = {
      name: draft.name.trim(),
      agent_id: agentId,
      action: { kind: 'mission' as const, mission },
      triggers: draftsToInput(draft.triggers),
      concurrency: draft.concurrency,
      max_concurrent: draft.concurrency === 'parallel' ? draft.maxConcurrent : 1,
      max_runs_per_hour: draft.maxRunsPerHour,
      continuity: draft.continuity,
      notes_enabled: draft.notesEnabled,
    }
    setBusy(true)
    setNameError(undefined)
    setTriggerErrors({})
    try {
      if (automation) {
        await patchAutomation(automation.id, {
          ...common,
          description: draft.description.trim(),
          enabled: draft.enabled,
          ...(draft.expiresLocal !== initial.expiresLocal && {
            expires_at: draft.expiresLocal ? localInputToIso(draft.expiresLocal) : null,
          }),
        })
        toast.success('Automation saved')
        navigate(`/automations/${automation.id}`)
      } else {
        const { id } = await createAutomation({
          ...common,
          description: draft.description.trim() || undefined,
          expires_at: draft.expiresLocal ? localInputToIso(draft.expiresLocal) : undefined,
        })
        toast.success('Automation created')
        navigate(`/automations/${id}`)
      }
    } catch (err) {
      const code = (err as { code?: string }).code
      const message = err instanceof Error ? err.message : String(err)
      if (code === 'name_conflict') {
        setNameError('An automation with this name already exists.')
      } else if (code === 'bad_cron') {
        const target = draft.triggers[triggerIndex(message)] ?? draft.triggers.find((t) => t.kind === 'cron')
        if (target) setTriggerErrors({ [target.key]: message.replace(/^triggers\[\d+\]:\s*/, '') })
      } else if (code === 'bad_request' && draft.triggers[triggerIndex(message)]) {
        const target = draft.triggers[triggerIndex(message)]
        setTriggerErrors({ [target.key]: message.replace(/^triggers\[\d+\]:\s*/, '') })
      } else {
        toast.error(editing ? 'Could not save automation' : 'Could not create automation', { description: errText(err) })
      }
    } finally {
      setBusy(false)
    }
  }

  const crumbs = editing
    ? [
        { label: 'Automations', href: '/automations' },
        { label: automation.name, href: `/automations/${automation.id}` },
        { label: 'Edit' },
      ]
    : [{ label: 'Automations', href: '/automations' }, { label: 'New automation' }]

  return (
    <PageShell width="form">
      <PageHeader
        title={editing ? `Edit ${automation.name}` : 'New automation'}
        breadcrumbs={crumbs}
        description="Triggers start a mission. Each run obeys the limits under Advanced."
      />

      <Form
        className="max-w-2xl"
        onSubmit={(e) => {
          e.preventDefault()
          void submit()
        }}
      >
        <FieldGroup title="Basics">
          <Field label="Name" error={errors.name}>
            <Input
              value={draft.name}
              onChange={(e) => {
                set({ name: e.target.value })
                setNameError(undefined)
              }}
              placeholder="weekly-digest"
              maxLength={64}
            />
          </Field>
          <Field label="Description" optional>
            <Textarea
              value={draft.description}
              onChange={(e) => set({ description: e.target.value })}
              rows={2}
              maxLength={1024}
            />
          </Field>
          <Field label="Agent" description="The agent every run starts as." error={errors.agent}>
            {(p) => (
              <Select value={agentId} onValueChange={(v) => set({ agentId: v })}>
                <SelectTrigger {...p} className="w-full">
                  <SelectValue placeholder="Pick an agent" />
                </SelectTrigger>
                <SelectContent>
                  {agents.map((a) => (
                    <SelectItem key={a.id} value={a.id}>
                      {a.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}
          </Field>
          {editing && (
            <div className="flex items-center gap-3">
              <Switch
                id="automation-enabled"
                checked={draft.enabled}
                onCheckedChange={(enabled) => set({ enabled })}
              />
              <label htmlFor="automation-enabled" className="text-sm">
                Enabled
              </label>
            </div>
          )}
          <Field label="Expires" optional description="Runs stop after this moment, in your local time.">
            {(p) => (
              <div className="flex gap-2">
                <Input
                  {...p}
                  type="datetime-local"
                  value={draft.expiresLocal}
                  onChange={(e) => set({ expiresLocal: e.target.value })}
                  className="flex-1"
                />
                <Button
                  type="button"
                  variant="outline"
                  disabled={!draft.expiresLocal}
                  onClick={() => set({ expiresLocal: '' })}
                >
                  Clear
                </Button>
              </div>
            )}
          </Field>
        </FieldGroup>

        <FieldGroup title="Triggers" description="Any enabled trigger starts a run.">
          <TriggerList
            value={draft.triggers}
            onChange={(triggers) => {
              set({ triggers })
              setTriggerErrors({})
            }}
            errors={triggerErrors}
            connectors={connectors}
            submitted={submitted}
          />
        </FieldGroup>

        <FieldGroup title="Action">
          <div className="space-y-2">
            <p className="text-sm leading-5 font-semibold">Runs</p>
            <SegmentedControl
              aria-label="Action"
              value="mission"
              onChange={() => undefined}
              options={[
                { value: 'mission', label: 'Mission' },
                { value: 'workflow', label: 'Workflow (Phase 4)', disabled: true },
              ]}
            />
          </div>
          <MissionActionFields
            value={draft.mission}
            onChange={(mission) => set({ mission })}
            routes={routes}
            destinations={destinations}
            attachments={draft.attachments}
            onAttachmentsChange={(attachments) => set({ attachments })}
            goalError={errors.goal}
            goalHint={goalHint(draft.triggers)}
          />
        </FieldGroup>

        <Collapsible open={showAdvanced} onOpenChange={setShowAdvanced}>
          <CollapsibleTrigger asChild>
            <Button type="button" variant="ghost" className="-ml-3.5">
              <ChevronRight aria-hidden className={cn('transition-transform', showAdvanced && 'rotate-90')} />
              Advanced
            </Button>
          </CollapsibleTrigger>
          <CollapsibleContent>
            <div className="mt-5">
              <FieldGroup>
                <div className="space-y-2">
                  <p className="text-sm leading-5 font-semibold">When a run is already active</p>
                  <SegmentedControl
                    aria-label="Concurrency"
                    value={draft.concurrency}
                    onChange={(v) => {
                      const concurrency = v as Concurrency
                      set({ concurrency, maxConcurrent: concurrency === 'parallel' ? draft.maxConcurrent : 1 })
                    }}
                    options={[
                      { value: 'skip', label: 'Skip' },
                      { value: 'queue', label: 'Queue' },
                      { value: 'parallel', label: 'Parallel' },
                    ]}
                  />
                  <p className="text-sm text-muted-foreground">{concurrencyHelp[draft.concurrency]}</p>
                  {draft.concurrency === 'queue' && hasCron && (
                    <p role="status" className="text-sm text-warning">
                      Queued runs start only after the active run finishes; a slow mission can delay the next
                      boundary.
                    </p>
                  )}
                </div>
                <div className="grid gap-5 sm:grid-cols-2">
                  <Field label="Max concurrent runs" description="1 to 3, parallel only.">
                    <Input
                      type="number"
                      min={1}
                      max={3}
                      value={draft.maxConcurrent}
                      disabled={draft.concurrency !== 'parallel'}
                      onChange={(e) => set({ maxConcurrent: Math.min(3, Math.max(1, Number(e.target.value) || 1)) })}
                    />
                  </Field>
                  <Field label="Max runs per hour" description="1 to 60." error={errors.rate}>
                    <Input
                      type="number"
                      min={1}
                      max={60}
                      value={Number.isNaN(draft.maxRunsPerHour) ? '' : draft.maxRunsPerHour}
                      onChange={(e) => set({ maxRunsPerHour: e.target.value === '' ? NaN : Number(e.target.value) })}
                    />
                  </Field>
                </div>
                <div className="flex items-start gap-3">
                  <Switch
                    id="automation-continuity"
                    checked={draft.continuity}
                    onCheckedChange={(continuity) => set({ continuity })}
                  />
                  <div>
                    <label htmlFor="automation-continuity" className="text-sm">
                      Pass the last result forward
                    </label>
                    <p className="text-xs text-muted-foreground">Each run starts with the previous run's result.</p>
                  </div>
                </div>
                <div className="flex items-start gap-3">
                  <Switch
                    id="automation-notes"
                    checked={draft.notesEnabled}
                    onCheckedChange={(notesEnabled) => set({ notesEnabled })}
                  />
                  <div>
                    <label htmlFor="automation-notes" className="text-sm">
                      Notes
                    </label>
                    <p className="text-xs text-muted-foreground">Runs can read and update up to 10 short notes.</p>
                  </div>
                </div>
              </FieldGroup>
            </div>
          </CollapsibleContent>
        </Collapsible>

        <FormActions>
          <Button
            type="button"
            variant="outline"
            disabled={busy}
            onClick={() => navigate(automation ? `/automations/${automation.id}` : '/automations')}
          >
            Cancel
          </Button>
          <Button type="submit" disabled={busy} aria-busy={busy}>
            {busy && <Spinner size="sm" label="Saving" className="text-current" />}
            {editing ? 'Save' : 'Create automation'}
          </Button>
        </FormActions>
      </Form>
    </PageShell>
  )
}
