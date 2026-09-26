import { ChevronRight, Plus, Trash2, X } from 'lucide-react'
import { useId, useState, type ComponentProps } from 'react'
import { Link } from 'react-router'

import type { AdminConnector, Channel } from '../../api/types'
import { cn } from '../../lib/utils'
import { Field } from '../timothy/field'
import { IconButton } from '../timothy/icon-button'
import { SegmentedControl } from '../timothy/segmented-control'
import { Button } from '../ui/button'
import { Checkbox } from '../ui/checkbox'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '../ui/collapsible'
import { Input } from '../ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../ui/select'
import {
  channelPattern,
  connectorEventKinds,
  filterPathError,
  githubConnectorOptions,
  maxAllowlist,
  maxFilters,
  maxLabels,
  maxPattern,
  patternError,
  repoError,
  type TriggerDraft,
  type WebhookScheme,
} from './triggerDrafts'

type Patch = (patch: Partial<TriggerDraft>) => void

// ChipsInput adds entries on Enter, comma or blur, up to max.
export function ChipsInput({
  value,
  onChange,
  max,
  split = /,/,
  itemLabel,
  className,
  ...inputProps
}: {
  value: string[]
  onChange: (next: string[]) => void
  max: number
  split?: RegExp
  itemLabel: string
} & Omit<ComponentProps<typeof Input>, 'value' | 'onChange'>) {
  const [text, setText] = useState('')
  const full = value.length >= max

  const commit = () => {
    const next = [...value]
    for (const part of text.split(split)) {
      const v = part.trim()
      if (v && !next.includes(v) && next.length < max) next.push(v)
    }
    setText('')
    if (next.length !== value.length) onChange(next)
  }

  return (
    <div className="space-y-2">
      <Input
        {...inputProps}
        className={className}
        value={text}
        disabled={full || inputProps.disabled}
        onChange={(e) => setText(e.target.value)}
        onBlur={commit}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ',') {
            e.preventDefault()
            commit()
          }
        }}
      />
      {value.length > 0 && (
        <div className="flex flex-wrap gap-2">
          {value.map((v) => (
            <span
              key={v}
              className="inline-flex max-w-56 items-center gap-1.5 rounded-md border border-border bg-muted py-1 pr-1.5 pl-2 text-xs"
            >
              <span className="truncate font-mono">{v}</span>
              <button
                type="button"
                onClick={() => onChange(value.filter((x) => x !== v))}
                aria-label={`Remove ${itemLabel} ${v}`}
                className="flex size-3.5 shrink-0 items-center justify-center rounded-full text-muted-foreground hover:text-foreground"
              >
                <X className="size-3" aria-hidden />
              </button>
            </span>
          ))}
        </div>
      )}
      <p className="text-xs text-muted-foreground tabular-nums">
        {value.length} of {max}
      </p>
    </div>
  )
}

// ConnectorEventFields edits a connector_event trigger: connector,
// repository, event kinds and labels.
export function ConnectorEventFields({
  draft,
  update,
  connectors,
}: {
  draft: TriggerDraft
  update: Patch
  connectors: AdminConnector[] | null
}) {
  const groupId = useId()
  const options = githubConnectorOptions(connectors ?? [], draft.connectorId)

  if (connectors !== null && options.length === 0) {
    return (
      <p className="text-sm text-muted-foreground">
        <Link to="/settings/connectors" className="underline underline-offset-2 hover:text-foreground">
          Add a GitHub connector first
        </Link>
      </p>
    )
  }

  const toggleEvent = (value: string, on: boolean) =>
    update({ events: on ? [...draft.events, value] : draft.events.filter((e) => e !== value) })

  return (
    <div className="space-y-5">
      <div className="grid gap-5 sm:grid-cols-2">
        <Field label="Connector">
          {(p) => (
            <Select value={draft.connectorId} onValueChange={(connectorId) => update({ connectorId })}>
              <SelectTrigger {...p} className="w-full">
                <SelectValue placeholder={connectors === null ? 'Loading connectors' : 'Pick a connector'} />
              </SelectTrigger>
              <SelectContent>
                {options.map((c) => (
                  <SelectItem key={c.id} value={c.id}>
                    {c.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
        </Field>
        <Field label="Repository" error={repoError(draft)}>
          <Input
            value={draft.repo}
            onChange={(e) => update({ repo: e.target.value })}
            placeholder="owner/name"
            className="font-mono"
          />
        </Field>
      </div>
      <div role="group" aria-labelledby={`${groupId}-events`} className="space-y-2">
        <p id={`${groupId}-events`} className="text-sm font-medium">
          Events
        </p>
        <div className="space-y-2">
          {connectorEventKinds.map((k) => {
            const id = `${groupId}-${k.value}`
            return (
              <div key={k.value} className="flex items-start gap-2">
                <Checkbox
                  id={id}
                  className="mt-0.5"
                  checked={draft.events.includes(k.value)}
                  onCheckedChange={(on) => toggleEvent(k.value, on === true)}
                />
                <label htmlFor={id} className="text-sm">
                  <span className="font-mono text-xs">{k.value}</span>{' '}
                  <span className="text-muted-foreground">{k.description}</span>
                </label>
              </div>
            )
          })}
        </div>
      </div>
      <Field label="Labels" optional description="Only events on items that carry one of these labels. Empty matches all.">
        {(p) => (
          <ChipsInput
            {...p}
            value={draft.labels}
            onChange={(labels) => update({ labels })}
            max={maxLabels}
            itemLabel="label"
            placeholder="needs-review"
          />
        )}
      </Field>
    </div>
  )
}

// ChannelFields edits a channel trigger: the channel, the pattern a
// paired sender's message must match, with a sample to try it on, and
// an optional chat.
export function ChannelFields({
  draft,
  update,
  channels,
}: {
  draft: TriggerDraft
  update: Patch
  channels: Channel[] | null
}) {
  const [sample, setSample] = useState('')

  if (channels !== null && channels.length === 0) {
    return (
      <p className="text-sm text-muted-foreground">
        <Link to="/settings/channels" className="underline underline-offset-2 hover:text-foreground">
          Add a channel first
        </Link>
      </p>
    )
  }

  const re = draft.pattern ? channelPattern(draft.pattern) : undefined
  const tried = sample.trim()
  return (
    <div className="space-y-5">
      <div className="grid gap-5 sm:grid-cols-2">
        <Field label="Channel">
          {(p) => (
            <Select value={draft.channelId} onValueChange={(channelId) => update({ channelId })}>
              <SelectTrigger {...p} className="w-full">
                <SelectValue placeholder={channels === null ? 'Loading channels' : 'Pick a channel'} />
              </SelectTrigger>
              <SelectContent>
                {(channels ?? []).map((c) => (
                  <SelectItem key={c.id} value={c.id}>
                    {c.name} ({c.kind})
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
        </Field>
        <Field label="Chat ID" optional description="Only messages from this chat. Empty matches every chat.">
          <Input
            value={draft.chatId}
            onChange={(e) => update({ chatId: e.target.value })}
            placeholder="-1001234567890"
            className="font-mono"
          />
        </Field>
      </div>
      <Field
        label="Pattern"
        description={`An RE2 regular expression, case-insensitive, at most ${maxPattern} characters. ^/run coverage$ matches only that message.`}
        error={patternError(draft)}
      >
        <Input
          value={draft.pattern}
          onChange={(e) => update({ pattern: e.target.value })}
          placeholder="^/run coverage$"
          className="font-mono"
        />
      </Field>
      <Field label="Try a message" optional>
        <Input value={sample} onChange={(e) => setSample(e.target.value)} placeholder="/run coverage" />
      </Field>
      {re && tried && (
        <p role="status" className="text-xs text-muted-foreground">
          {re.test(tried) ? 'matches' : 'no match'}: <span className="font-mono">{tried}</span>
        </p>
      )}
    </div>
  )
}

// WebhookFields edits a webhook trigger: signature scheme, signing
// secret name and body filters.
export function WebhookFields({ draft, update, error }: { draft: TriggerDraft; update: Patch; error?: string }) {
  const setFilter = (i: number, patch: Partial<{ path: string; equals: string }>) =>
    update({ filters: draft.filters.map((f, j) => (j === i ? { ...f, ...patch } : f)) })

  return (
    <div className="space-y-5">
      <div className="space-y-2">
        <p className="text-sm font-medium">Signature</p>
        <SegmentedControl
          aria-label="Signature scheme"
          size="sm"
          value={draft.scheme}
          onChange={(v) => update({ scheme: v as WebhookScheme })}
          options={[
            { value: 'github', label: 'GitHub' },
            { value: 'generic', label: 'Generic' },
          ]}
        />
      </div>
      <Field
        label="Signing secret"
        description="The name of the stored secret that holds the signing key, such as HOOK_SIGNING_KEY."
        error={error}
      >
        <Input
          value={draft.credentialRef}
          onChange={(e) => update({ credentialRef: e.target.value })}
          placeholder="HOOK_SIGNING_KEY"
          className="font-mono"
          autoComplete="off"
        />
      </Field>
      <div className="space-y-2">
        <p className="text-sm font-medium">Filters</p>
        <p className="text-sm text-muted-foreground">A delivery runs only when every filter matches its JSON body.</p>
        {draft.filters.map((f, i) => {
          const pathError = filterPathError(f.path)
          return (
            <div key={i} role="group" aria-label={`Filter ${i + 1}`} className="space-y-1.5">
              <div className="flex items-center gap-2">
                <Input
                  aria-label={`Filter ${i + 1} path`}
                  aria-invalid={pathError ? true : undefined}
                  value={f.path}
                  onChange={(e) => setFilter(i, { path: e.target.value })}
                  placeholder="action"
                  className="font-mono"
                />
                <Input
                  aria-label={`Filter ${i + 1} equals`}
                  value={f.equals}
                  onChange={(e) => setFilter(i, { equals: e.target.value })}
                  placeholder="opened"
                />
                <IconButton
                  label={`Remove filter ${i + 1}`}
                  icon={Trash2}
                  size="sm"
                  onClick={() => update({ filters: draft.filters.filter((_, j) => j !== i) })}
                />
              </div>
              {pathError && (
                <p role="alert" className="text-xs text-destructive">
                  {pathError}
                </p>
              )}
            </div>
          )
        })}
        <div className="flex items-center gap-3">
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={draft.filters.length >= maxFilters}
            onClick={() => update({ filters: [...draft.filters, { path: '', equals: '' }] })}
          >
            <Plus aria-hidden />
            Add filter
          </Button>
          <span className="text-xs text-muted-foreground tabular-nums">
            {draft.filters.length} of {maxFilters}
          </span>
        </div>
      </div>
    </div>
  )
}

// ToolAllowlistField narrows the tools a run from this trigger gets;
// empty leaves the agent's tools as they are.
export function ToolAllowlistField({ draft, update, index }: { draft: TriggerDraft; update: Patch; index: number }) {
  const list = draft.toolAllowlist ?? []
  const [open, setOpen] = useState(list.length > 0)
  return (
    <Collapsible open={open} onOpenChange={setOpen}>
      <CollapsibleTrigger asChild>
        <Button type="button" variant="ghost" size="sm" className="-ml-2.5" aria-label={`Tool allowlist, trigger ${index + 1}`}>
          <ChevronRight aria-hidden className={cn('transition-transform', open && 'rotate-90')} />
          Tool allowlist
          {list.length > 0 && <span className="text-xs text-muted-foreground tabular-nums">{list.length}</span>}
        </Button>
      </CollapsibleTrigger>
      <CollapsibleContent>
        <div className="mt-3">
          <Field label="Tools" optional description="Runs from this trigger get only these of the agent's tools; tools the agent lacks are dropped. Empty keeps them all. Delegated coding harnesses need shell and write_file.">
            {(p) => (
              <ChipsInput
                {...p}
                value={list}
                onChange={(toolAllowlist) => update({ toolAllowlist })}
                max={maxAllowlist}
                split={/[\s,]+/}
                itemLabel="tool"
                placeholder="search_mail"
                className="font-mono"
              />
            )}
          </Field>
        </div>
      </CollapsibleContent>
    </Collapsible>
  )
}
