import type { AutomationTriggerInput } from '../../api/client'
import type { AdminConnector, AutomationTrigger, AutomationTriggerConfig, WebhookFilter } from '../../api/types'
import { cronPresets, isCronShape } from '../../lib/cron'

export type WebhookScheme = 'github' | 'generic'

// TriggerDraft is one editable trigger row; key is local, id is the
// server's and keeps the trigger's state on save.
export interface TriggerDraft {
  key: string
  id?: string
  kind: AutomationTrigger['kind']
  expr: string
  enabled: boolean
  toolAllowlist?: string[]
  connectorId: string
  repo: string
  events: string[]
  labels: string[]
  scheme: WebhookScheme
  filters: WebhookFilter[]
  credentialRef: string
  // secretMode/secretValue are webhook-only: 'new' names and pastes a
  // secret to write on save, 'existing' just picks a stored name.
  secretMode: 'new' | 'existing'
  secretValue: string
  channelId: string
  pattern: string
  chatId: string
}

export const maxTriggers = 5
export const maxLabels = 10
export const maxFilters = 10
export const maxAllowlist = 64
export const maxPattern = 200
export const defaultCron = cronPresets[0].cron

// connectorEventKinds mirrors events.ConnectorKinds on the server.
export const connectorEventKinds = [
  { value: 'pr.opened', description: 'A pull request is opened.' },
  { value: 'pr.labeled', description: 'A label is added to a pull request.' },
  { value: 'pr.review', description: 'A review is submitted on a pull request.' },
  { value: 'pr.review_comment', description: 'Someone comments on a pull request diff.' },
  { value: 'issue.comment', description: 'Someone comments on an issue or pull request.' },
  { value: 'check.completed', description: 'A check run finishes.' },
] as const

const repoShape = /^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/
const filterPathShape = /^(\$\.)?[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+)*$/
// secretNameShape mirrors the server's stored-secret name rule.
export const secretNameShape = /^[A-Za-z0-9_./-]{1,128}$/

let seq = 0
const nextKey = () => `trigger-${++seq}`

const blankFields = {
  connectorId: '',
  repo: '',
  events: [] as string[],
  labels: [] as string[],
  scheme: 'github' as WebhookScheme,
  filters: [] as WebhookFilter[],
  credentialRef: '',
  secretMode: 'new' as const,
  secretValue: '',
  channelId: '',
  pattern: '',
  chatId: '',
}

// newTriggerDraft is a fresh daily cron trigger.
export function newTriggerDraft(): TriggerDraft {
  return { key: nextKey(), kind: 'cron', expr: defaultCron, enabled: true, ...blankFields }
}

// draftsFromTriggers seeds rows from stored or template triggers.
export function draftsFromTriggers(
  triggers: {
    id?: string
    kind: AutomationTrigger['kind']
    config: AutomationTriggerConfig
    enabled?: boolean
    credential_ref?: string
    tool_allowlist?: string[]
  }[],
): TriggerDraft[] {
  return triggers.map((t) => ({
    key: nextKey(),
    id: t.id,
    kind: t.kind,
    expr: t.config.expr ?? '',
    enabled: t.enabled ?? true,
    toolAllowlist: t.tool_allowlist,
    connectorId: t.config.connector_id ?? '',
    repo: t.config.repo ?? '',
    events: t.config.events ?? [],
    labels: t.config.labels ?? [],
    scheme: t.config.scheme ?? 'github',
    filters: (t.config.filters ?? []).map((f) => ({ path: f.path.replace(/^\$\./, ''), equals: f.equals })),
    credentialRef: t.credential_ref ?? '',
    secretMode: t.credential_ref ? 'existing' : 'new',
    secretValue: '',
    channelId: t.config.channel_id ?? '',
    pattern: t.config.pattern ?? '',
    chatId: t.config.chat_id ?? '',
  }))
}

function configFor(d: TriggerDraft): AutomationTriggerConfig {
  switch (d.kind) {
    case 'cron':
      return { expr: d.expr.trim() }
    case 'connector_event':
      return {
        connector_id: d.connectorId,
        repo: d.repo.trim(),
        events: d.events,
        ...(d.labels.length > 0 && { labels: d.labels }),
      }
    case 'webhook':
      return {
        scheme: d.scheme,
        ...(d.filters.length > 0 && { filters: d.filters.map((f) => ({ path: f.path.trim(), equals: f.equals })) }),
      }
    case 'channel':
      return { channel_id: d.channelId, pattern: d.pattern, ...(d.chatId.trim() && { chat_id: d.chatId.trim() }) }
    default:
      return {}
  }
}

// draftsToInput builds the wire triggers; manual triggers carry no
// config and only webhook triggers carry a credential_ref. secretValue
// never goes on the wire: the caller writes it via setSecret first.
export function draftsToInput(drafts: TriggerDraft[]): AutomationTriggerInput[] {
  return drafts.map((d) => ({
    ...(d.id && { id: d.id }),
    kind: d.kind,
    config: configFor(d),
    ...(d.kind === 'webhook' && { credential_ref: d.credentialRef.trim() }),
    enabled: d.enabled,
    ...(d.toolAllowlist && d.toolAllowlist.length > 0 && { tool_allowlist: d.toolAllowlist }),
  }))
}

// cronError is the client-side message for a draft, or undefined.
export function cronError(d: TriggerDraft): string | undefined {
  if (d.kind !== 'cron' || isCronShape(d.expr)) return undefined
  return 'Use five fields: minute, hour, day of month, month, weekday.'
}

// repoError flags a connector event repo that is not owner/name.
export function repoError(d: TriggerDraft): string | undefined {
  if (d.kind !== 'connector_event' || d.repo.trim() === '' || repoShape.test(d.repo.trim())) return undefined
  return 'Use owner/name, such as octo/timothy.'
}

// filterPathError flags a webhook filter path the server would reject.
export function filterPathError(path: string): string | undefined {
  if (path.trim() === '' || filterPathShape.test(path.trim())) return undefined
  return 'Use a dotted path, such as action or pull_request.user.login.'
}

// channelPattern compiles a channel trigger pattern the way the server
// matches it (case-insensitive); undefined when JavaScript cannot parse
// it. The server's RE2 has the final say.
export function channelPattern(pattern: string): RegExp | undefined {
  try {
    return new RegExp(pattern.replace(/^\(\?i\)/, ''), 'i')
  } catch {
    return undefined
  }
}

// patternError flags a channel pattern the server would reject.
export function patternError(d: TriggerDraft): string | undefined {
  if (d.kind !== 'channel' || d.pattern === '') return undefined
  if (d.pattern.length > maxPattern) return `Keep the pattern under ${maxPattern} characters.`
  if (!channelPattern(d.pattern)) return 'Not a valid regular expression.'
  return undefined
}

// secretFieldError flags a webhook draft's secret name/value, mode
// aware: 'new' needs a valid name and a pasted key, 'existing' needs a
// picked stored name.
export function secretFieldError(d: TriggerDraft): string | undefined {
  if (d.secretMode === 'existing') {
    return d.credentialRef.trim() === '' ? 'Pick a stored secret.' : undefined
  }
  const name = d.credentialRef.trim()
  if (name === '') return 'Enter the signing secret name.'
  if (!secretNameShape.test(name)) return 'Use letters, digits, _ . / or -.'
  if (d.secretValue.trim() === '') return 'Paste the signing key.'
  return undefined
}

// triggerError reports whether a draft is incomplete or malformed, as
// one message; the fields show their own errors.
export function triggerError(d: TriggerDraft): string | undefined {
  if (d.kind === 'cron') return cronError(d)
  if (d.kind === 'connector_event') {
    if (!d.connectorId) return 'Pick a GitHub connector.'
    if (d.repo.trim() === '') return 'Enter a repository.'
    if (repoError(d)) return repoError(d)
    if (d.events.length === 0) return 'Pick at least one event.'
  }
  if (d.kind === 'webhook') {
    const secretErr = secretFieldError(d)
    if (secretErr) return secretErr
    const bad = d.filters.find((f) => f.path.trim() === '' || filterPathError(f.path))
    if (bad) return 'Fix the filter paths.'
  }
  if (d.kind === 'channel') {
    if (!d.channelId) return 'Pick a channel.'
    if (d.pattern === '') return 'Enter a pattern.'
    return patternError(d)
  }
  return undefined
}

// pendingSecrets lists the new-mode webhook secrets a submit must
// write before the automation POST, deduped by name (last write wins).
export function pendingSecrets(drafts: TriggerDraft[]): { name: string; value: string }[] {
  const byName = new Map<string, string>()
  for (const d of drafts) {
    if (d.kind !== 'webhook' || d.secretMode !== 'new') continue
    const name = d.credentialRef.trim()
    if (name === '' || d.secretValue === '') continue
    byName.set(name, d.secretValue)
  }
  return Array.from(byName, ([name, value]) => ({ name, value }))
}

// githubConnectorOptions lists enabled github connectors, plus the
// picked one so an edit keeps showing a connector that was disabled.
export function githubConnectorOptions(connectors: AdminConnector[], picked: string): AdminConnector[] {
  return connectors.filter((c) => c.kind === 'github' && (c.enabled || c.id === picked))
}

const defaultGoalHint = 'Use {{event.pr_url}} or {{notes.name}} to insert trigger data or notes.'

// goalHint lists the goal placeholders the draft's triggers supply.
export function goalHint(drafts: TriggerDraft[]): string {
  const parts: string[] = []
  if (drafts.some((d) => d.kind === 'connector_event')) {
    parts.push('{{event.repo}}, {{event.number}}, {{event.url}}, {{event.title}} or {{event.author}} for GitHub events')
  }
  if (drafts.some((d) => d.kind === 'webhook')) {
    parts.push('{{event.body.<field>}} or {{event.delivery}} for webhooks')
  }
  if (drafts.some((d) => d.kind === 'channel')) {
    parts.push('{{event.text}}, {{event.sender}} or {{event.chat_id}} for channel messages')
  }
  if (parts.length === 0) return defaultGoalHint
  return `Use ${parts.join(', ')}, and {{notes.name}} for notes.`
}
