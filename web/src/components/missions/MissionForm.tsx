import { useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router'
import { toast } from 'sonner'
import {
  classifyMission,
  type CreateMissionInput,
  createConnectorRepo,
  createMission,
  createSchedule,
  type ExecutorOption,
  getMissionExecutionPlan,
  getMissionExecutorOptions,
  getSettings,
  listConnectorRepos,
  listConnectors,
  listDestinations,
  listKbCollections,
  patchSchedule,
  testConnector,
} from '../../api/client'
import type {
  AdminAgent,
  AdminConnector,
  AdminRoute,
  Destination,
  ExecutionPlanPhase,
  GitHubIdentity,
  GitHubRepo,
  KbCollection,
  Reference,
  Schedule,
} from '../../api/types'
import { useAgents, useRoutes } from '../AgentPicker'
import { slugify } from '../settings/AgentForm'
import { cronPresets, type CronPresetValue, presetFor } from '../../lib/schedules'
import { CURRENCIES } from '../../lib/currencies'
import { Badge } from '../ui/badge'
import { Button } from '../ui/button'
import { Calendar } from '../ui/calendar'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '../ui/collapsible'
import { Command, CommandEmpty, CommandInput, CommandItem, CommandList } from '../ui/command'
import { Input } from '../ui/input'
import { Label } from '../ui/label'
import { Popover, PopoverContent, PopoverTrigger } from '../ui/popover'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../ui/select'
import { Textarea } from '../ui/textarea'
import { errText } from '../settings/util'
import { envIcon } from '../icons/EnvIcons'
import { type PendingAttachment } from '../Composer'
import { MissionAttachments } from './MissionAttachments'
import { GoalTextarea } from './MissionReferences'
import { MissionExecutionPlan } from './MissionExecutionPlan'

type RepoSource = 'none' | 'github'

type Kind = 'coding' | 'general'

type OnComplete = '' | 'push' | 'push_pr'

// Sentinel for the Deployment Select: Radix Select.Item rejects an
// empty string value, so the "do nothing" choice (the wire value '')
// is represented by this sentinel on the Select itself.
const ON_COMPLETE_NONE = '__none__'

const onCompleteChoices: { value: string; label: string }[] = [
  { value: ON_COMPLETE_NONE, label: 'Nothing' },
  { value: 'push', label: 'Push branch when done' },
  { value: 'push_pr', label: 'Push and open a PR when done' },
]

// repoFromCloneURL rebuilds a minimal GitHubRepo from a parent
// mission's stored clone URL (mirrors MissionDetail.tsx's
// githubFullName): a follow-up seeds the repo picker with the
// parent's repo, but the mission row only ever stored the clone URL,
// not repos.list's other fields (private/default_branch/etc), which
// this form's fields never read for an already-selected repo.
function repoFromCloneURL(cloneURL: string): GitHubRepo {
  return {
    full_name: cloneURL.replace(/^https?:\/\/[^/]+\//, '').replace(/\.git$/, ''),
    private: false,
    default_branch: '',
    html_url: cloneURL.replace(/\.git$/, ''),
    clone_url: cloneURL,
    pushed_at: '',
  }
}

const kindCopy: Record<Kind, string> = {
  coding: 'Coding · branches from repo',
  general: 'General · scratch workspace',
}

// A schedule's mission_template carries route/review_route as explicit
// undefined when unset (see types.ts): "non-default" means any of
// them, or a non-default agent, actually has a value. Budget and
// auto-approve are always visible now, so they never force the
// advanced section open.
function hasNonDefaults(t: Schedule['mission_template']): boolean {
  return !!(
    t.agent_id ||
    t.route ||
    t.review_route ||
    t.plan_route ||
    t.harness ||
    t.environment ||
    t.branch_pattern ||
    t.commit_style
  )
}

// Radix Select.Item rejects an empty string value, so the "no route
// chosen" state is represented by this sentinel on the wire between the
// Select and the route/reviewRoute/escalationRoute state (which stay ''
// to match the API's own empty-means-default semantics).
const ROUTE_DEFAULT = '__default__'

// defaultRouteLabel mirrors the server's own empty-route resolution
// (internal/brain/api/missions.go's CreateMission handler +
// missions.DefaultCodingRoute) so the Route select's "Default" option
// tells the operator where it actually resolves, in the same order the
// server tries:
//  1. the picked agent's own route, if it has one
//  2. for a coding mission, a route literally named "coding" if one exists
//  3. whichever route carries the "default" role
//  4. otherwise just "Default"
export function defaultRouteLabel(
  kind: Kind,
  agent: AdminAgent | undefined,
  routes: AdminRoute[] | null,
): string {
  if (agent?.route) return `Default (${agent.route})`
  if (kind === 'coding' && routes?.some((r) => r.name === 'coding')) return 'Default (coding)'
  const defaultRoleRoute = routes?.find((r) => r.role === 'default')
  if (defaultRoleRoute) return `Default (${defaultRoleRoute.name})`
  return 'Default'
}

// resolvedDefaultRoute is defaultRouteLabel's same precedence, but
// returns the route name (or '' when none resolves: matches the
// server's own '' == "no real route, gateway default chain" case)
// instead of a display label. Used to fetch executor-options against
// the route a create would actually use, not always the system
// default.
function resolvedDefaultRoute(
  kind: Kind,
  agent: AdminAgent | undefined,
  routes: AdminRoute[] | null,
): string {
  if (agent?.route) return agent.route
  if (kind === 'coding' && routes?.some((r) => r.name === 'coding')) return 'coding'
  return routes?.find((r) => r.role === 'default')?.name ?? ''
}

// Sentinel for the executor Select's "apply the settings default"
// choice: wire value stays '' (omit harness from the create payload)
// to match the API's own empty-means-default semantics. Exported so
// AgentForm's Harness select can reuse the same sentinel/choice list
// for its own "inherit" option.
export const EXECUTOR_DEFAULT = '__default__'

// executorChoices maps a harness Select value to its label: easy to
// extend as more harnesses register. Exported so MissionExecutionPlan
// can reuse the same harness->label mapping for the axis column.
export const executorChoices: { value: string; label: string }[] = [
  { value: EXECUTOR_DEFAULT, label: 'Default (from settings)' },
  { value: 'native', label: 'Native' },
  { value: 'claude-cli', label: 'Claude Code' },
  { value: 'pi', label: 'pi' },
  { value: 'codex-cli', label: 'Codex CLI' },
  { value: 'opencode', label: 'OpenCode' },
  { value: 'cursor-cli', label: 'Cursor CLI' },
]

// defaultHarnessLabel names what the Harness select's "Default" choice
// actually resolves to: mirrors defaultRouteLabel's job for Route:
// so an operator can tell what runs without opening Settings. Falls
// back to the plain choice when defaultHarnessName is empty (fetch
// failed) or names a harness not in executorChoices (native, or one
// not yet in this list).
function defaultHarnessLabel(defaultHarnessName: string): string {
  if (!defaultHarnessName) return 'Default (from settings)'
  const known = executorChoices.find((c) => c.value === defaultHarnessName)
  return `Default (${known?.label ?? defaultHarnessName})`
}

// defaultPlanRouteLabel names what leaving Plan route on "Default"
// resolves to, read from the execution plan response's plan phase:
// never recomputed client-side. Falls back to the plain hardcoded
// string before the plan has loaded.
function defaultPlanRouteLabel(plan: ExecutionPlanPhase[] | null): string {
  const phase = plan?.find((p) => p.phase === 'plan')
  if (!phase?.route) return 'Same as execute route'
  return `Same as execute route (${phase.route})`
}

// defaultReviewRouteLabel names what leaving Review route on
// "Default" resolves to: either the plan or the execute route,
// whichever the review phase actually inherited from.
function defaultReviewRouteLabel(plan: ExecutionPlanPhase[] | null): string {
  const phase = plan?.find((p) => p.phase === 'review')
  if (!phase?.route) return 'Default (same as plan/execute route)'
  const from = phase.route_source === 'inherited-from-execute' ? 'execute' : 'plan'
  return `Same as ${from} route (${phase.route})`
}

// defaultEscalationRouteLabel names the Escalation route select's
// "Off" choice with the trigger condition, always the same text since
// escalation only ever resolves to a route when one is set explicitly.
function defaultEscalationRouteLabel(): string {
  return 'Off (no escalation on failure)'
}

// Sentinel for the environment Select's "auto-detect" choice: wire
// value stays '' (omit environment from the create payload) to match
// the API's own empty-means-auto-detect semantics (D-05x).
const ENVIRONMENT_AUTO = '__auto__'

// environmentChoices maps an environment Select value to its label:
// mirrors sandboxd's image allowlist (internal/sandboxd/manager.go).
const environmentChoices: { value: string; label: string }[] = [
  { value: ENVIRONMENT_AUTO, label: 'Auto-detect' },
  { value: 'base', label: 'Base' },
  { value: 'go', label: 'Go' },
  { value: 'node', label: 'Node' },
  { value: 'python', label: 'Python' },
  { value: 'java', label: 'Java' },
  { value: 'php', label: 'PHP' },
]

// Sentinel for the commit-style Select's "apply the settings default"
// choice: wire value stays '' (omit commit_style from the create
// payload) to match the API's own empty-means-default semantics.
const COMMIT_STYLE_DEFAULT = '__default__'

const commitStyleChoices: { value: string; label: string }[] = [
  { value: COMMIT_STYLE_DEFAULT, label: 'Default (from settings)' },
  { value: 'conventional', label: 'Conventional' },
  { value: 'plain', label: 'Plain' },
]

// expiresAt is stored as the wire-compatible 'YYYY-MM-DDTHH:mm' string the
// API already expects; these split it into a Date (for the calendar) and a
// 'HH:mm' string (for the time input) and back.
function expiresAtToDate(v: string): Date | undefined {
  if (!v) return undefined
  const [datePart, timePart] = v.split('T')
  const [y, m, d] = datePart.split('-').map(Number)
  const [h, min] = (timePart ?? '00:00').split(':').map(Number)
  return new Date(y, m - 1, d, h, min)
}

function expiresAtToTime(v: string): string {
  return v.split('T')[1] ?? '00:00'
}

function dateAndTimeToExpiresAt(date: Date, time: string): string {
  const y = date.getFullYear()
  const m = String(date.getMonth() + 1).padStart(2, '0')
  const d = String(date.getDate()).padStart(2, '0')
  return `${y}-${m}-${d}T${time}`
}

function formatExpiresAt(v: string): string {
  const date = expiresAtToDate(v)
  if (!date) return 'Never'
  return `${date.toLocaleDateString(undefined, {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
  })}, ${expiresAtToTime(v)}`
}

// MissionForm is the shared form behind both the new-mission page and
// the edit-schedule page. In 'create' mode it submits a one-off
// mission or, with "Repeat on schedule" on, a new schedule. In 'edit'
// mode it always patches the given schedule and locks out run-once,
// coding, and the escalation route: the same constraints
// NewMissionDialog/ScheduleDialog enforced.
export function MissionForm({
  mode,
  schedule,
  initial,
  parentMissionId,
  onDone,
  onCancel,
}: {
  mode: 'create' | 'edit'
  schedule?: Schedule
  // initial seeds the form's create-mode state from a parent mission
  // (a follow-up): everything except goal, which is left empty for
  // the user to type. Read only once, in the useState initializers
  // below, so the caller must render this component only once initial
  // is settled (e.g. after an async parent fetch resolves).
  initial?: Partial<CreateMissionInput>
  // parentMissionId, when set, is included on the create payload:
  // makes this a follow-up mission (see CreateMissionInput).
  parentMissionId?: string
  onDone: (result: { kind: 'mission' | 'schedule'; id: string }) => void
  onCancel: () => void
}) {
  const agents = useAgents()
  const routes = useRoutes()
  const enabledRoutes = routes?.filter((r) => r.enabled) ?? []
  const [goal, setGoal] = useState('')
  const goalWordCount = useMemo(
    () => (goal.trim() === '' ? 0 : goal.trim().split(/\s+/).length),
    [goal],
  )
  // attachments, like goal, is never seeded from initial: a follow-up
  // carries the parent's outcome digest as prompt context, not its
  // documents; each new mission attaches its own.
  const [attachments, setAttachments] = useState<PendingAttachment[]>([])
  // references picked via the goal field's # mentions: component
  // state only, resolved server-side at create time.
  const [references, setReferences] = useState<Reference[]>([])
  const [kind, setKind] = useState<Kind>(initial?.kind ?? 'general')
  // kindLocked freezes kind against further auto-classify calls once
  // the user has explicitly chosen it (chip click, or the repeat-mode
  // general override below): cleared when the goal is emptied, which
  // resets to auto-detect for whatever's typed next. A follow-up's
  // seeded kind counts as an explicit choice too.
  const [kindLocked, setKindLocked] = useState(!!initial?.kind)
  const [classifying, setClassifying] = useState(false)
  // light (D-069): defaults from the debounced classify preview below
  // unless the operator has touched the toggle directly (lightTouched).
  const [light, setLight] = useState(initial?.light ?? false)
  const [lightTouched, setLightTouched] = useState(!!initial?.light)
  const [agentID, setAgentID] = useState(initial?.agent_id ?? '')
  // Destinations multi-select: visible, not advanced; default is
  // empty (deliver nowhere). Offered for a one-off create, a new
  // schedule (repeat on), and editing an existing schedule: fetched
  // once per form regardless of mode.
  const [destinations, setDestinations] = useState<Destination[] | null>(null)
  const [destinationIDs, setDestinationIDs] = useState<string[]>([])
  useEffect(() => {
    listDestinations()
      .then(setDestinations)
      .catch(() => {
        // Non-fatal: the section just stays hidden if this fails, same
        // degrade as any other optional list fetch in this form.
      })
  }, [])
  // Promote-to-KB collection picker (D-081, issue #370): one-off create
  // only, same visibility/fetch-failure reasoning as destinations above.
  const [kbCollections, setKbCollections] = useState<KbCollection[] | null>(null)
  const [promoteKBCollectionID, setPromoteKBCollectionID] = useState(
    initial?.promote_kb_collection_id ?? '',
  )
  useEffect(() => {
    listKbCollections()
      .then(setKbCollections)
      .catch(() => {
        // Non-fatal: the section just stays hidden if this fails.
      })
  }, [])
  const [showAdvanced, setShowAdvanced] = useState(false)
  const [route, setRoute] = useState(initial?.route ?? '')
  const [reviewRoute, setReviewRoute] = useState(initial?.review_route ?? '')
  const [planRoute, setPlanRoute] = useState(initial?.plan_route ?? '')
  const [escalationRoute, setEscalationRoute] = useState(initial?.escalation_route ?? '')
  const [routeModel, setRouteModel] = useState(initial?.route_model ?? '')
  const [planRouteModel, setPlanRouteModel] = useState(initial?.plan_route_model ?? '')
  const [reviewRouteModel, setReviewRouteModel] = useState(initial?.review_route_model ?? '')
  const [budget, setBudget] = useState('')
  const [budgetCurrency, setBudgetCurrency] = useState('USD')
  const [autoApproveSafe, setAutoApproveSafe] = useState(true)
  const [harness, setHarness] = useState(initial?.harness ?? '')
  const [environment, setEnvironment] = useState(initial?.environment ?? '')
  const [branchPattern, setBranchPattern] = useState(initial?.branch_pattern ?? '')
  const [commitStyle, setCommitStyle] = useState(initial?.commit_style ?? '')
  const [executorOptions, setExecutorOptions] = useState<ExecutorOption[] | null>(null)
  const [executionPlan, setExecutionPlan] = useState<ExecutionPlanPhase[] | null>(null)
  // defaultHarnessName is settings' coding_executor value (the harness
  // a create actually runs when the Harness select is left on
  // "Default"): fetched once so that choice can say what it resolves
  // to, the same way the Route select's "Default" already names the
  // route it resolves to (defaultRouteLabel).
  const [defaultHarnessName, setDefaultHarnessName] = useState('')
  const [busy, setBusy] = useState(false)

  // Repository source: 'none' self-initializes an empty repo (the
  // existing coding-mission default); 'github' clones an existing repo
  // (or a brand-new one) through a github-kind connector.
  const [repoSource, setRepoSource] = useState<RepoSource>(initial?.repo_url ? 'github' : 'none')
  const [githubConnectors, setGithubConnectors] = useState<AdminConnector[] | null>(null)
  const [connectorID, setConnectorID] = useState(initial?.connector_id ?? '')
  const [repos, setRepos] = useState<GitHubRepo[] | null>(null)
  const [reposLoading, setReposLoading] = useState(false)
  const [reposError, setReposError] = useState<string | null>(null)
  const [repoQuery, setRepoQuery] = useState('')
  const [selectedRepo, setSelectedRepo] = useState<GitHubRepo | null>(
    initial?.repo_url ? repoFromCloneURL(initial.repo_url) : null,
  )
  const [connIdentity, setConnIdentity] = useState<GitHubIdentity | null>(null)
  const [repoPickerOpen, setRepoPickerOpen] = useState(false)
  const [newRepo, setNewRepo] = useState(false)
  const [newRepoName, setNewRepoName] = useState('')
  const [newRepoPrivate, setNewRepoPrivate] = useState(true)

  // Consent-at-create for the mission's auto-completion action:
  // resets whenever the GitHub source is unpicked, since on_complete is
  // only ever valid alongside repo_url/connector_id.
  const [onComplete, setOnComplete] = useState<OnComplete>(initial?.on_complete ?? '')
  useEffect(() => {
    if (repoSource !== 'github') setOnComplete('')
  }, [repoSource])

  // Fetch github-kind connectors once the operator picks the GitHub
  // source: most missions never touch this, so it's not loaded
  // upfront with agents/routes.
  useEffect(() => {
    if (repoSource !== 'github' || githubConnectors !== null) return
    listConnectors()
      .then((all) => setGithubConnectors(all.filter((c) => c.kind === 'github' && c.enabled)))
      .catch(() => setGithubConnectors([]))
  }, [repoSource, githubConnectors])

  // Fetch the connector's repo list whenever it changes: best-effort,
  // an error surfaces inline rather than blocking the form.
  useEffect(() => {
    if (repoSource !== 'github' || !connectorID) {
      setRepos(null)
      return
    }
    setReposLoading(true)
    setReposError(null)
    listConnectorRepos(connectorID)
      .then(setRepos)
      .catch((err) => setReposError(errText(err)))
      .finally(() => setReposLoading(false))
  }, [repoSource, connectorID])

  // Resolve the connection's GitHub identity so the Deployment section
  // can say who commits and PRs will be authored as: best-effort, an
  // unresolved identity just hides the line.
  useEffect(() => {
    if (repoSource !== 'github' || !connectorID) {
      setConnIdentity(null)
      return
    }
    testConnector(connectorID)
      .then((res) => setConnIdentity(res.identity ?? null))
      .catch(() => setConnIdentity(null))
  }, [repoSource, connectorID])

  const filteredRepos = useMemo(() => {
    if (!repos) return []
    const q = repoQuery.trim().toLowerCase()
    if (!q) return repos
    return repos.filter((r) => r.full_name.toLowerCase().includes(q))
  }, [repos, repoQuery])

  // Live executor pairing/usability preview: coding-only, refetched
  // whenever the kind flips to coding or the route selection (explicit
  // or resolved default) changes. An explicit route override wins;
  // otherwise this asks about the same route a create would actually
  // resolve to (resolvedDefaultRoute), not always the system default:
  // else a "coding" route's own chain never gets previewed. Best-effort
  //: a failed fetch degrades to a plain, fully-enabled select with no
  // live info, the server validates on submit anyway.
  useEffect(() => {
    if (kind !== 'coding') {
      setExecutorOptions(null)
      return
    }
    const effectiveRoute = route || resolvedDefaultRoute(kind, agents.find((a) => a.id === agentID), routes)
    getMissionExecutorOptions(effectiveRoute || undefined)
      .then(setExecutorOptions)
      .catch(() => setExecutorOptions(null))
  }, [kind, route, agentID, agents, routes])

  // Live execution plan: the server-resolved read-out of what each
  // phase actually runs, keyed on every field that affects resolution.
  // Best-effort: a failed fetch just hides the table, the server is
  // still the source of truth at create time.
  useEffect(() => {
    getMissionExecutionPlan({
      kind,
      agent: agentID || undefined,
      harness: harness || undefined,
      route: route || undefined,
      plan_route: planRoute || undefined,
      review_route: reviewRoute || undefined,
      escalation_route: escalationRoute || undefined,
      route_model: routeModel || undefined,
      plan_route_model: planRouteModel || undefined,
      review_route_model: reviewRouteModel || undefined,
      light: kind === 'general' ? light : undefined,
    })
      .then(setExecutionPlan)
      .catch(() => setExecutionPlan(null))
  }, [
    kind,
    agentID,
    harness,
    route,
    planRoute,
    reviewRoute,
    escalationRoute,
    routeModel,
    planRouteModel,
    reviewRouteModel,
    light,
  ])

  // Pre-select the settings page's configured default currency for a
  // fresh create: edit mode below overwrites this with the schedule's
  // own saved currency once it loads.
  useEffect(() => {
    if (mode !== 'create') return
    getSettings()
      .then((s) => {
        const v = s.values.default_currency
        if (v) setBudgetCurrency(v)
      })
      .catch(() => {
        // Best-effort: falls back to the USD default already set.
      })
  }, [mode])

  // Read-only, both modes: names the harness the settings-page
  // coding_executor value resolves to, purely for the Harness select's
  // "Default" label. Best-effort: an empty value just shows the plain
  // "Default (from settings)" fallback.
  useEffect(() => {
    getSettings()
      .then((s) => setDefaultHarnessName(s.values.coding_executor ?? ''))
      .catch(() => {
        // Best-effort: falls back to the plain "Default" label.
      })
  }, [])

  // Repeat-on-schedule fields: read/submitted whenever repeat is on
  // (create mode) or always (edit mode, which only ever edits a
  // schedule).
  const [repeat, setRepeat] = useState(mode === 'edit')
  const [scheduleName, setScheduleName] = useState('')
  const [preset, setPreset] = useState<CronPresetValue>('daily-7am')
  const [cron, setCron] = useState<string>(cronPresets[0].cron ?? '0 7 * * *')
  const [cronError, setCronError] = useState<string | null>(null)
  const [maxIterations, setMaxIterations] = useState('')
  const [expiresAt, setExpiresAt] = useState('')

  // Edit mode: re-seed every time the schedule to edit changes.
  useEffect(() => {
    if (mode !== 'edit' || !schedule) return
    setScheduleName(schedule.name)
    setCron(schedule.cron)
    setPreset(presetFor(schedule.cron))
    setGoal(schedule.mission_template.goal)
    setKind(schedule.mission_template.kind)
    setKindLocked(true)
    setAgentID(schedule.mission_template.agent_id ?? '')
    setLight(schedule.mission_template.light ?? false)
    setLightTouched(true)
    setAutoApproveSafe(schedule.mission_template.auto_approve_safe ?? true)
    setShowAdvanced(hasNonDefaults(schedule.mission_template))
    setRoute(schedule.mission_template.route ?? '')
    setReviewRoute(schedule.mission_template.review_route ?? '')
    setPlanRoute(schedule.mission_template.plan_route ?? '')
    setMaxIterations(
      schedule.mission_template.max_iterations != null
        ? String(schedule.mission_template.max_iterations)
        : '',
    )
    setBudget(
      schedule.mission_template.budget_amount != null
        ? String(schedule.mission_template.budget_amount)
        : '',
    )
    setBudgetCurrency(schedule.mission_template.budget_currency || 'USD')
    setHarness(schedule.mission_template.harness ?? '')
    setEnvironment(schedule.mission_template.environment ?? '')
    setBranchPattern(schedule.mission_template.branch_pattern ?? '')
    setCommitStyle(schedule.mission_template.commit_style ?? '')
    setDestinationIDs(schedule.mission_template.destination_ids ?? [])
    setExpiresAt(schedule.expires_at ? schedule.expires_at.slice(0, 16) : '')
    setCronError(null)
  }, [mode, schedule])

  // Live kind inference: debounced 600ms after goal edits, skipped
  // once the user has locked a manual choice or the goal is empty.
  // Unlocking on an emptied goal happens in the textarea's onChange
  // below (a direct user edit), not here: this effect also runs on
  // mount/schedule-load with the PREVIOUS render's goal, and clearing
  // the lock from here would race the schedule-seed effect's own
  // setKindLocked(true) and stomp it back to false.
  useEffect(() => {
    if (goal.trim() === '' || kindLocked) return
    setClassifying(true)
    const t = setTimeout(() => {
      classifyMission(goal.trim())
        .then((r) => {
          setKind(r.kind)
          // light only ever defaults the toggle, never overrides an
          // operator's own choice (lightTouched): and only makes sense
          // once the goal actually classified as general.
          if (!lightTouched) setLight(r.kind === 'general' && r.light)
        })
        .catch(() => {
          // Best-effort preview: a failed classify leaves whatever kind
          // was already showing rather than blocking the form.
        })
        .finally(() => setClassifying(false))
    }, 600)
    return () => {
      clearTimeout(t)
      setClassifying(false)
    }
  }, [goal, kindLocked, lightTouched])

  const onGoalChange = (v: string) => {
    setGoal(v)
    if (v.trim() === '') setKindLocked(false)
  }

  const toggleKind = () => {
    if (repeat && kind === 'general') return // coding is unavailable while repeating
    setKind((k) => (k === 'coding' ? 'general' : 'coding'))
    setKindLocked(true)
  }

  const pickAgent = (id: string) => {
    setAgentID(id)
    const agent = agents.find((a) => a.id === id)
    if (agent) {
      setReviewRoute(agent.review_route ?? '')
    }
  }

  const pickPreset = (v: CronPresetValue) => {
    setPreset(v)
    const found = cronPresets.find((p) => p.value === v)
    if (found?.cron) setCron(found.cron)
  }

  const pickExpiresDate = (date: Date | undefined) => {
    if (!date) return
    setExpiresAt(dateAndTimeToExpiresAt(date, expiresAtToTime(expiresAt) || '00:00'))
  }

  const pickExpiresTime = (time: string) => {
    const date = expiresAtToDate(expiresAt) ?? new Date()
    setExpiresAt(dateAndTimeToExpiresAt(date, time))
  }

  // A client-side 5-field shape check only: the server is the
  // authoritative cron validator (robfig/cron), this just catches
  // obvious typos before a round trip.
  const validCronShape = (v: string) => v.trim().split(/\s+/).length === 5

  const onCronChange = (v: string) => {
    setCron(v)
    setCronError(null)
  }

  // A GitHub source is only meaningful once it can actually resolve to
  // a clone URL: an existing repo picked, or a new-repo name typed.
  const githubSourceReady =
    repoSource !== 'github' ||
    !!connectorID && (newRepo ? newRepoName.trim() !== '' : !!selectedRepo)

  const canSubmit =
    mode === 'edit'
      ? scheduleName.trim() !== '' && goal.trim() !== '' && validCronShape(cron)
      : goal.trim() !== '' &&
        (!repeat || validCronShape(cron)) &&
        githubSourceReady &&
        !attachments.some((a) => a.uploading)

  const submitMission = async () => {
    let repoURL: string | undefined
    if (kind === 'coding' && repoSource === 'github' && connectorID) {
      const repo = newRepo
        ? await createConnectorRepo(connectorID, {
            name: newRepoName.trim(),
            private: newRepoPrivate,
          })
        : selectedRepo
      repoURL = repo?.clone_url
    }
    const { id } = await createMission({
      goal: goal.trim(),
      kind,
      agent_id: agentID || undefined,
      route: route || undefined,
      review_route: reviewRoute || undefined,
      plan_route: planRoute || undefined,
      escalation_route: escalationRoute || undefined,
      route_model: routeModel || undefined,
      plan_route_model: planRouteModel || undefined,
      review_route_model: reviewRouteModel || undefined,
      budget_amount: budget ? Number(budget) : undefined,
      budget_currency: budget ? budgetCurrency : undefined,
      auto_approve_safe: autoApproveSafe,
      harness: kind === 'coding' ? harness || undefined : undefined,
      environment: kind === 'coding' ? environment || undefined : undefined,
      branch_pattern: kind === 'coding' ? branchPattern.trim() || undefined : undefined,
      commit_style: kind === 'coding' ? commitStyle || undefined : undefined,
      repo_url: repoURL,
      connector_id: repoURL ? connectorID : undefined,
      on_complete: repoURL && onComplete ? onComplete : undefined,
      parent_mission_id: parentMissionId,
      attachments:
        attachments.length > 0 ? attachments.map((a) => ({ id: a.id, name: a.name ?? '' })) : undefined,
      destination_ids: destinationIDs.length > 0 ? destinationIDs : undefined,
      promote_kb_collection_id: promoteKBCollectionID || undefined,
      light: kind === 'general' ? light : undefined,
      references:
        references.length > 0 ? references.map((r) => ({ kind: r.kind, id: r.id })) : undefined,
    })
    toast.success('Mission created')
    onDone({ kind: 'mission', id })
  }

  const submitSchedule = async () => {
    const { id } = await createSchedule({
      name: slugify(scheduleName || goal),
      cron,
      mission_template: {
        goal: goal.trim(),
        kind,
        agent_id: agentID || undefined,
        route: route || undefined,
        review_route: reviewRoute || undefined,
        plan_route: planRoute || undefined,
        max_iterations: maxIterations ? Number(maxIterations) : undefined,
        budget_amount: budget ? Number(budget) : undefined,
        budget_currency: budget ? budgetCurrency : undefined,
        auto_approve_safe: autoApproveSafe,
        harness: kind === 'coding' ? harness || undefined : undefined,
        environment: kind === 'coding' ? environment || undefined : undefined,
        branch_pattern: kind === 'coding' ? branchPattern.trim() || undefined : undefined,
        commit_style: kind === 'coding' ? commitStyle || undefined : undefined,
        destination_ids: destinationIDs.length > 0 ? destinationIDs : undefined,
        light: kind === 'general' ? light : undefined,
      },
      expires_at: expiresAt ? new Date(expiresAt).toISOString() : undefined,
    })
    toast.success('Schedule created')
    onDone({ kind: 'schedule', id })
  }

  const submitEdit = async () => {
    if (!schedule) return
    const sc = await patchSchedule(schedule.id, {
      name: slugify(scheduleName),
      cron,
      mission_template: {
        goal: goal.trim(),
        kind: schedule.mission_template.kind,
        agent_id: agentID || undefined,
        route: route || undefined,
        review_route: reviewRoute || undefined,
        plan_route: planRoute || undefined,
        max_iterations: maxIterations ? Number(maxIterations) : undefined,
        budget_amount: budget ? Number(budget) : undefined,
        budget_currency: budget ? budgetCurrency : undefined,
        auto_approve_safe: autoApproveSafe,
        harness: schedule.mission_template.kind === 'coding' ? harness || undefined : undefined,
        environment:
          schedule.mission_template.kind === 'coding' ? environment || undefined : undefined,
        branch_pattern:
          schedule.mission_template.kind === 'coding'
            ? branchPattern.trim() || undefined
            : undefined,
        commit_style:
          schedule.mission_template.kind === 'coding' ? commitStyle || undefined : undefined,
        destination_ids: destinationIDs.length > 0 ? destinationIDs : undefined,
        light: schedule.mission_template.kind === 'general' ? light : undefined,
      },
      expires_at: expiresAt ? new Date(expiresAt).toISOString() : null,
    })
    toast.success('Schedule updated')
    onDone({ kind: 'schedule', id: sc.id })
  }

  const submit = async () => {
    if (repeat && !validCronShape(cron)) {
      setCronError('Cron must have 5 space-separated fields (minute hour day month weekday).')
      return
    }
    setBusy(true)
    try {
      if (mode === 'edit') {
        await submitEdit()
      } else if (repeat) {
        await submitSchedule()
      } else {
        await submitMission()
      }
    } catch (err) {
      const label = mode === 'edit' ? 'update' : repeat ? 'create' : 'create'
      const noun = mode === 'edit' || repeat ? 'schedule' : 'mission'
      toast.error(`Could not ${label} ${noun}`, { description: errText(err) })
    } finally {
      setBusy(false)
    }
  }

  const submitLabel = mode === 'edit' ? 'Save schedule' : repeat ? 'Create schedule' : 'Create mission'

  return (
    <div className="space-y-8">
      <section className="space-y-3">
        <div>
          <h2 className="text-sm font-semibold">Goal</h2>
          <p className="text-sm text-muted-foreground">
            What should this mission accomplish{repeat ? ' each time it fires' : ''}?
          </p>
        </div>
        {mode === 'create' && !repeat ? (
          <GoalTextarea
            id="mission-goal"
            aria-label="Goal"
            value={goal}
            onChange={onGoalChange}
            references={references}
            onReferences={setReferences}
            placeholder="What should this mission accomplish? Markdown supported. Type # to reference a mission, chat, or document."
            rows={10}
            autoFocus
            className="min-h-60 resize-y text-base"
          />
        ) : (
          <Textarea
            id="mission-goal"
            aria-label="Goal"
            value={goal}
            onChange={(e) => onGoalChange(e.target.value)}
            placeholder="What should this mission accomplish? Markdown supported."
            rows={10}
            autoFocus
            className="min-h-60 resize-y text-base"
          />
        )}
        <p className="text-right text-xs text-muted-foreground">
          {goalWordCount} {goalWordCount === 1 ? 'word' : 'words'}
        </p>

        {goal.trim() !== '' && (
          <button type="button" onClick={toggleKind} disabled={repeat && kind === 'general'}>
            <Badge variant={classifying ? 'outline' : 'secondary'} className="cursor-pointer">
              {classifying ? 'Detecting…' : kindCopy[kind]}
            </Badge>
          </button>
        )}
        {repeat && (
          <p className="text-xs text-muted-foreground">
            Coding missions aren't supported on a recurring schedule yet: each fire has no
            repository to work in.
          </p>
        )}
        {kind === 'general' && (
          <label htmlFor="mission-light" className="flex items-start gap-2 text-sm">
            <input
              id="mission-light"
              type="checkbox"
              checked={light}
              onChange={(e) => {
                setLight(e.target.checked)
                setLightTouched(true)
              }}
              className="mt-0.5"
            />
            <span>
              Light mission
              <span className="block text-xs text-muted-foreground">
                Skips explore/plan/review for a single-pass task — the worker's final message
                is delivered as the result.
              </span>
            </span>
          </label>
        )}
        {mode === 'create' && !repeat && (
          <MissionAttachments attachments={attachments} onChange={setAttachments} />
        )}
      </section>

      {kind === 'coding' && mode === 'create' && !repeat && (
        <section className="space-y-3">
          <div>
            <h2 className="text-sm font-semibold">Repository</h2>
            <p className="text-sm text-muted-foreground">
              Work in a fresh scratch repo, or clone an existing GitHub repo.
            </p>
          </div>

          <div className="inline-flex rounded-lg bg-muted p-1 text-sm">
            <button
              type="button"
              onClick={() => setRepoSource('none')}
              aria-pressed={repoSource === 'none'}
              className={`rounded-md px-3 py-1.5 font-medium transition ${
                repoSource === 'none' ? 'bg-background shadow-sm' : 'text-muted-foreground'
              }`}
            >
              None
            </button>
            <button
              type="button"
              onClick={() => setRepoSource('github')}
              aria-pressed={repoSource === 'github'}
              className={`rounded-md px-3 py-1.5 font-medium transition ${
                repoSource === 'github' ? 'bg-background shadow-sm' : 'text-muted-foreground'
              }`}
            >
              GitHub
            </button>
          </div>

          {repoSource === 'github' && (
            <div className="space-y-3 rounded-lg border border-border p-4">
              {githubConnectors === null ? (
                <p className="text-sm text-muted-foreground">Loading connectors…</p>
              ) : githubConnectors.length === 0 ? (
                <p className="text-sm text-muted-foreground">
                  No GitHub connectors configured yet.{' '}
                  <Link to="/settings/connectors" className="underline underline-offset-2">
                    Add one in Settings → Connectors
                  </Link>
                  .
                </p>
              ) : (
                <>
                  <div className="space-y-1.5">
                    <Label htmlFor="mission-connector">Connector</Label>
                    <Select
                      value={connectorID}
                      onValueChange={(v) => {
                        setConnectorID(v)
                        setSelectedRepo(null)
                        setRepoQuery('')
                      }}
                    >
                      <SelectTrigger id="mission-connector" className="w-full">
                        <SelectValue placeholder="Choose a connector" />
                      </SelectTrigger>
                      <SelectContent>
                        {githubConnectors.map((c) => (
                          <SelectItem key={c.id} value={c.id}>
                            {c.name}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </div>

                  {connectorID && !newRepo && (
                    <div className="space-y-1.5">
                      <Label>Repository</Label>
                      <Popover open={repoPickerOpen} onOpenChange={setRepoPickerOpen}>
                        <PopoverTrigger asChild>
                          <Button
                            variant="outline"
                            className="w-full justify-start font-normal"
                            disabled={reposLoading}
                          >
                            {reposLoading
                              ? 'Loading repos…'
                              : (selectedRepo?.full_name ?? 'Choose a repository')}
                          </Button>
                        </PopoverTrigger>
                        <PopoverContent className="w-(--radix-popover-trigger-width) p-0">
                          <Command shouldFilter={false}>
                            <CommandInput
                              placeholder="Filter repositories…"
                              value={repoQuery}
                              onValueChange={setRepoQuery}
                            />
                            <CommandList>
                              <CommandEmpty>
                                {reposError ? `Could not load repos: ${reposError}` : 'No matches.'}
                              </CommandEmpty>
                              {filteredRepos.map((r) => (
                                <CommandItem
                                  key={r.full_name}
                                  value={r.full_name}
                                  onSelect={() => {
                                    setSelectedRepo(r)
                                    setRepoPickerOpen(false)
                                  }}
                                >
                                  <span className="truncate">{r.full_name}</span>
                                  {r.private && (
                                    <Badge variant="outline" className="ml-auto shrink-0">
                                      Private
                                    </Badge>
                                  )}
                                </CommandItem>
                              ))}
                            </CommandList>
                          </Command>
                        </PopoverContent>
                      </Popover>
                    </div>
                  )}

                  {connectorID && (
                    <label
                      htmlFor="mission-new-repo"
                      className="flex items-center gap-2 text-sm"
                    >
                      <input
                        id="mission-new-repo"
                        type="checkbox"
                        checked={newRepo}
                        onChange={(e) => {
                          setNewRepo(e.target.checked)
                          setSelectedRepo(null)
                        }}
                      />
                      New repository
                    </label>
                  )}

                  {connectorID && newRepo && (
                    <div className="grid gap-3 sm:grid-cols-[1fr_auto]">
                      <div className="space-y-1.5">
                        <Label htmlFor="mission-new-repo-name">Name</Label>
                        <Input
                          id="mission-new-repo-name"
                          value={newRepoName}
                          onChange={(e) => setNewRepoName(e.target.value)}
                          placeholder="my-new-repo"
                        />
                      </div>
                      <label
                        htmlFor="mission-new-repo-private"
                        className="flex items-end gap-2 pb-2 text-sm"
                      >
                        <input
                          id="mission-new-repo-private"
                          type="checkbox"
                          checked={newRepoPrivate}
                          onChange={(e) => setNewRepoPrivate(e.target.checked)}
                        />
                        Private
                      </label>
                    </div>
                  )}
                </>
              )}
            </div>
          )}

          {repoSource === 'github' && githubSourceReady && (
            <div className="space-y-1.5">
              <Label htmlFor="mission-on-complete">Deployment</Label>
              <Select
                value={onComplete || ON_COMPLETE_NONE}
                onValueChange={(v) =>
                  setOnComplete(v === ON_COMPLETE_NONE ? '' : (v as OnComplete))
                }
              >
                <SelectTrigger id="mission-on-complete" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {onCompleteChoices.map((c) => (
                    <SelectItem key={c.value} value={c.value}>
                      {c.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <p className="text-xs text-muted-foreground">
                What happens automatically when the mission finishes. A human always chooses this
                up front — the model never decides.
              </p>
              {connIdentity && (
                <p className="text-xs text-muted-foreground">
                  Commits and pull requests will be authored as{' '}
                  <span className="font-medium text-foreground">
                    {connIdentity.name || connIdentity.login}
                  </span>{' '}
                  ({connIdentity.email})
                </p>
              )}
            </div>
          )}
        </section>
      )}

      <section className="space-y-3">
        <div>
          <h2 className="text-sm font-semibold">When it runs</h2>
          <p className="text-sm text-muted-foreground">
            {mode === 'edit'
              ? 'This schedule fires the mission above on the cron below.'
              : 'Run it now, or fire it on a recurring schedule instead.'}
          </p>
        </div>

        {mode === 'create' && (
          <div className="inline-flex rounded-lg bg-muted p-1 text-sm">
            <button
              type="button"
              onClick={() => setRepeat(false)}
              aria-pressed={!repeat}
              className={`rounded-md px-3 py-1.5 font-medium transition ${
                !repeat ? 'bg-background shadow-sm' : 'text-muted-foreground'
              }`}
            >
              Run once
            </button>
            <button
              type="button"
              onClick={() => {
                setRepeat(true)
                if (kind === 'coding') {
                  setKind('general')
                  setKindLocked(true)
                }
              }}
              aria-pressed={repeat}
              className={`rounded-md px-3 py-1.5 font-medium transition ${
                repeat ? 'bg-background shadow-sm' : 'text-muted-foreground'
              }`}
            >
              Repeat on schedule
            </button>
          </div>
        )}

        {repeat && (
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-1.5">
              <Label htmlFor="mission-schedule-name">Schedule name</Label>
              <Input
                id="mission-schedule-name"
                value={scheduleName}
                onChange={(e) => setScheduleName(e.target.value)}
                placeholder={slugify(goal) || 'schedule name'}
              />
            </div>

            <div className="space-y-1.5">
              <Label>Runs</Label>
              <Select value={preset} onValueChange={(v) => pickPreset(v as CronPresetValue)}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {cronPresets.map((p) => (
                    <SelectItem key={p.value} value={p.value}>
                      {p.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              {preset === 'custom' && (
                <Input
                  aria-label="Cron expression"
                  value={cron}
                  onChange={(e) => onCronChange(e.target.value)}
                  placeholder="0 7 * * *"
                  className="font-mono"
                />
              )}
              {cronError && <p className="text-xs text-destructive">{cronError}</p>}
            </div>

            <div className="space-y-1.5">
              <Label htmlFor="mission-expires">Expires</Label>
              <Popover>
                <PopoverTrigger asChild>
                  <Button id="mission-expires" variant="outline" className="w-full justify-start">
                    {formatExpiresAt(expiresAt)}
                  </Button>
                </PopoverTrigger>
                <PopoverContent className="w-auto p-0">
                  <Calendar
                    mode="single"
                    selected={expiresAtToDate(expiresAt)}
                    onSelect={pickExpiresDate}
                  />
                  <div className="flex items-center gap-2 border-t border-border p-2.5">
                    <Input
                      aria-label="Expires time"
                      type="time"
                      value={expiresAtToTime(expiresAt)}
                      onChange={(e) => pickExpiresTime(e.target.value)}
                      disabled={!expiresAt}
                      className="h-8.5 flex-1"
                    />
                    <Button variant="outline" size="sm" onClick={() => setExpiresAt('')}>
                      Clear
                    </Button>
                  </div>
                </PopoverContent>
              </Popover>
              <p className="text-xs text-muted-foreground">
                Server time. The schedule stops firing after this moment. Empty means it never
                expires.
              </p>
            </div>
          </div>
        )}
      </section>

      <section className="space-y-3">
        <div>
          <h2 className="text-sm font-semibold">Agent &amp; limits</h2>
          <p className="text-sm text-muted-foreground">Who runs it, and what it's allowed to spend.</p>
        </div>

        {agents.length > 0 && (
          <div className="space-y-1.5">
            <Label>Agent</Label>
            <Select value={agentID} onValueChange={pickAgent}>
              <SelectTrigger className="w-full">
                <SelectValue placeholder="Default" />
              </SelectTrigger>
              <SelectContent>
                {agents.map((a: AdminAgent) => (
                  <SelectItem key={a.id} value={a.id}>
                    {a.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        )}

        {kind === 'coding' && (
          <div className="space-y-1.5">
            <Label htmlFor="mission-executor">Harness</Label>
            <Select
              value={harness || EXECUTOR_DEFAULT}
              onValueChange={(v) => setHarness(v === EXECUTOR_DEFAULT ? '' : v)}
            >
              <SelectTrigger id="mission-executor" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {executorChoices.map((c) => {
                  const opt = executorOptions?.find((o) => o.harness === c.value)
                  const disabled = !!opt && !opt.usable
                  const label = c.value === EXECUTOR_DEFAULT ? defaultHarnessLabel(defaultHarnessName) : c.label
                  return (
                    <SelectItem
                      key={c.value}
                      value={c.value}
                      disabled={disabled}
                      title={disabled ? opt?.reason : undefined}
                    >
                      {label}
                    </SelectItem>
                  )
                })}
              </SelectContent>
            </Select>
          </div>
        )}

        {kind === 'coding' && (
          <div className="space-y-1.5">
            <Label htmlFor="mission-environment">Environment</Label>
            <Select
              value={environment || ENVIRONMENT_AUTO}
              onValueChange={(v) => setEnvironment(v === ENVIRONMENT_AUTO ? '' : v)}
            >
              <SelectTrigger id="mission-environment" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {environmentChoices.map((c) => {
                  const EnvIcon = envIcon(c.value)
                  return (
                    <SelectItem key={c.value} value={c.value}>
                      {EnvIcon && <EnvIcon />}
                      {c.label}
                    </SelectItem>
                  )
                })}
              </SelectContent>
            </Select>
          </div>
        )}

        <div className="space-y-1.5">
          <Label htmlFor="mission-budget">Budget</Label>
          <div className="flex gap-2">
            <Input
              id="mission-budget"
              type="number"
              value={budget}
              onChange={(e) => setBudget(e.target.value)}
              placeholder="No limit"
              className="flex-1"
            />
            <Select value={budgetCurrency} onValueChange={setBudgetCurrency}>
              <SelectTrigger className="w-24" aria-label="Budget currency">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {CURRENCIES.map((c) => (
                  <SelectItem key={c} value={c}>
                    {c}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        </div>

        <label htmlFor="mission-auto-approve" className="flex items-start gap-2 text-sm">
          <input
            id="mission-auto-approve"
            type="checkbox"
            checked={autoApproveSafe}
            onChange={(e) => setAutoApproveSafe(e.target.checked)}
            className="mt-0.5"
          />
          <span>
            Auto-approve safe tool calls
            <span className="block text-xs text-muted-foreground">
              Runs unattended without pausing for approval on routine commands. Destructive
              or unrecognized commands still always ask.
            </span>
          </span>
        </label>

        {destinations && destinations.length > 0 && (
          <div className="space-y-1.5">
            <Label>Deliver results to</Label>
            <div className="space-y-1.5 rounded-xl border border-border p-3">
              {destinations.map((d) => (
                <label key={d.id} htmlFor={`mission-destination-${d.id}`} className="flex items-center gap-2 text-sm">
                  <input
                    id={`mission-destination-${d.id}`}
                    type="checkbox"
                    checked={destinationIDs.includes(d.id)}
                    onChange={(e) =>
                      setDestinationIDs((prev) =>
                        e.target.checked ? [...prev, d.id] : prev.filter((id) => id !== d.id),
                      )
                    }
                  />
                  <span>{d.name}</span>
                  <span className="text-xs text-muted-foreground uppercase">{d.kind}</span>
                </label>
              ))}
            </div>
          </div>
        )}

        {mode === 'create' && !repeat && kbCollections && kbCollections.length > 0 && (
          <div className="space-y-1.5">
            <Label htmlFor="mission-promote-kb">Promote to knowledge base on done</Label>
            <select
              id="mission-promote-kb"
              value={promoteKBCollectionID}
              onChange={(e) => setPromoteKBCollectionID(e.target.value)}
              className="w-full rounded-md border border-border bg-background px-3 py-2 text-sm"
            >
              <option value="">Don't promote automatically</option>
              {kbCollections.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}
                </option>
              ))}
            </select>
          </div>
        )}

        <MissionExecutionPlan
          plan={executionPlan}
          routeModel={routeModel}
          onRouteModelChange={setRouteModel}
          planRouteModel={planRouteModel}
          onPlanRouteModelChange={setPlanRouteModel}
          reviewRouteModel={reviewRouteModel}
          onReviewRouteModelChange={setReviewRouteModel}
        />

        <Collapsible open={showAdvanced} onOpenChange={setShowAdvanced}>
          <CollapsibleTrigger className="text-xs text-muted-foreground underline underline-offset-2 hover:text-foreground">
            {showAdvanced ? 'Hide advanced options' : 'Show advanced options'}
          </CollapsibleTrigger>
          <CollapsibleContent>
            <div className="mt-3 grid gap-4 rounded-lg border border-border p-4 sm:grid-cols-2">
              <div className="space-y-1.5">
                <Label htmlFor="mission-route">Route</Label>
                {routes === null ? (
                  <Input
                    id="mission-route"
                    value={route}
                    onChange={(e) => setRoute(e.target.value)}
                    placeholder="default"
                  />
                ) : (
                  <Select
                    value={route || ROUTE_DEFAULT}
                    onValueChange={(v) => setRoute(v === ROUTE_DEFAULT ? '' : v)}
                  >
                    <SelectTrigger id="mission-route" className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value={ROUTE_DEFAULT}>
                        {defaultRouteLabel(kind, agents.find((a) => a.id === agentID), routes)}
                      </SelectItem>
                      {enabledRoutes.map((r) => (
                        <SelectItem key={r.name} value={r.name}>
                          {r.name}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                )}
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="mission-review-route">Review route</Label>
                {routes === null ? (
                  <Input
                    id="mission-review-route"
                    value={reviewRoute}
                    onChange={(e) => setReviewRoute(e.target.value)}
                    placeholder="default"
                  />
                ) : (
                  <Select
                    value={reviewRoute || ROUTE_DEFAULT}
                    onValueChange={(v) => setReviewRoute(v === ROUTE_DEFAULT ? '' : v)}
                  >
                    <SelectTrigger id="mission-review-route" className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value={ROUTE_DEFAULT}>{defaultReviewRouteLabel(executionPlan)}</SelectItem>
                      {enabledRoutes.map((r) => (
                        <SelectItem key={r.name} value={r.name}>
                          {r.name}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                )}
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="mission-plan-route">Plan route</Label>
                {routes === null ? (
                  <Input
                    id="mission-plan-route"
                    value={planRoute}
                    onChange={(e) => setPlanRoute(e.target.value)}
                    placeholder="Same as execute route"
                  />
                ) : (
                  <Select
                    value={planRoute || ROUTE_DEFAULT}
                    onValueChange={(v) => setPlanRoute(v === ROUTE_DEFAULT ? '' : v)}
                  >
                    <SelectTrigger id="mission-plan-route" className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value={ROUTE_DEFAULT}>{defaultPlanRouteLabel(executionPlan)}</SelectItem>
                      {enabledRoutes.map((r) => (
                        <SelectItem key={r.name} value={r.name}>
                          {r.name}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                )}
                <p className="text-xs text-muted-foreground">
                  Explore, plan, and review run on this route instead of the execute route above —
                  e.g. a strong model plans while a cheap/local route executes.
                </p>
              </div>
              {mode === 'create' && !repeat && (
                <div className="space-y-1.5">
                  <Label htmlFor="mission-escalation-route">Escalation route</Label>
                  {routes === null ? (
                    <Input
                      id="mission-escalation-route"
                      value={escalationRoute}
                      onChange={(e) => setEscalationRoute(e.target.value)}
                      placeholder="Off, set to switch route after a failed or reworked turn"
                    />
                  ) : (
                    <Select
                      value={escalationRoute || ROUTE_DEFAULT}
                      onValueChange={(v) => setEscalationRoute(v === ROUTE_DEFAULT ? '' : v)}
                    >
                      <SelectTrigger id="mission-escalation-route" className="w-full">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value={ROUTE_DEFAULT}>{defaultEscalationRouteLabel()}</SelectItem>
                        {enabledRoutes.map((r) => (
                          <SelectItem key={r.name} value={r.name}>
                            {r.name}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  )}
                </div>
              )}
              {kind === 'coding' && (
                <div className="space-y-1.5">
                  <Label htmlFor="mission-branch-pattern">Branch pattern</Label>
                  <Input
                    id="mission-branch-pattern"
                    value={branchPattern}
                    onChange={(e) => setBranchPattern(e.target.value)}
                    placeholder="Default (from settings)"
                  />
                </div>
              )}
              {kind === 'coding' && (
                <div className="space-y-1.5">
                  <Label htmlFor="mission-commit-style">Commit style</Label>
                  <Select
                    value={commitStyle || COMMIT_STYLE_DEFAULT}
                    onValueChange={(v) => setCommitStyle(v === COMMIT_STYLE_DEFAULT ? '' : v)}
                  >
                    <SelectTrigger id="mission-commit-style" className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {commitStyleChoices.map((c) => (
                        <SelectItem key={c.value} value={c.value}>
                          {c.label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
              )}
              {repeat && (
                <div className="space-y-1.5">
                  <Label htmlFor="mission-max-iterations">Max iterations</Label>
                  <Input
                    id="mission-max-iterations"
                    type="number"
                    value={maxIterations}
                    onChange={(e) => setMaxIterations(e.target.value)}
                    placeholder="Default"
                  />
                </div>
              )}
            </div>
          </CollapsibleContent>
        </Collapsible>
      </section>

      <div className="flex gap-2">
        <Button variant="outline" disabled={busy} onClick={onCancel}>
          Cancel
        </Button>
        <Button disabled={!canSubmit || busy} onClick={() => void submit()}>
          {submitLabel}
        </Button>
      </div>
    </div>
  )
}
