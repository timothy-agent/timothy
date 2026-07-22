import { useState } from 'react'
import { Input } from '../ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '../ui/select'
import { Field, Toggle } from './shared'
import type { AdminAgent, AdminRoute } from '../../api/types'

// slugify mirrors the backend's name rule: lowercase slug.
export function slugify(v: string): string {
  return v
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
}

// splitList parses a comma-separated allowlist; empty = everything.
function splitList(v: string): string[] {
  return v
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)
}

export interface AgentFormValue {
  name: string
  description: string
  overlay: string
  route: string
  skills: string[]
  tools: string[]
  memory: boolean
}

export function useAgentForm(agent?: AdminAgent) {
  const [name, setName] = useState(agent?.name ?? '')
  const [description, setDescription] = useState(agent?.description ?? '')
  const [overlay, setOverlay] = useState(agent?.prompt_overlay ?? '')
  const [route, setRoute] = useState(agent?.route ?? '')
  const [skillsText, setSkillsText] = useState(agent?.skills.join(', ') ?? '')
  const [toolsText, setToolsText] = useState(agent?.tools.join(', ') ?? '')
  const [memory, setMemory] = useState(agent?.memory ?? true)

  const value: AgentFormValue = {
    name,
    description: description.trim(),
    overlay,
    route,
    skills: splitList(skillsText),
    tools: splitList(toolsText),
    memory,
  }

  return {
    value,
    canSubmit: agent ? true : slugify(name) !== '',
    fields: { name, setName, description, setDescription, overlay, setOverlay, route, setRoute, skillsText, setSkillsText, toolsText, setToolsText, memory, setMemory },
  }
}

// AgentForm renders the shared field set for both create and edit —
// name is a one-time slug fixed at creation (it lives in ledger rows
// and event payloads), so it's the only field Add shows that Edit
// doesn't.
export function AgentForm({
  isNew,
  routes,
  fields,
}: {
  isNew: boolean
  routes: AdminRoute[]
  fields: ReturnType<typeof useAgentForm>['fields']
}) {
  return (
    <div className="grid gap-5">
      {isNew && (
        <Field label="Name" hint="unique slug, immutable after creation">
          <Input
            value={fields.name}
            onChange={(e) => fields.setName(e.target.value)}
            placeholder="infra, homelab, writer…"
            className="mt-1.5 h-10"
          />
        </Field>
      )}
      <Field label="Description" hint="shown in the picker">
        <Input
          value={fields.description}
          onChange={(e) => fields.setDescription(e.target.value)}
          placeholder="What this agent is for"
          className="mt-1.5 h-10"
        />
      </Field>
      <Field label="Prompt overlay" hint="appended to the system prompt">
        <textarea
          value={fields.overlay}
          onChange={(e) => fields.setOverlay(e.target.value)}
          aria-label="Prompt overlay"
          rows={5}
          placeholder="Instructions, persona, house rules…"
          className="mt-1.5 w-full rounded-lg border border-input bg-transparent p-3 text-sm outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50"
        />
      </Field>
      <div className="grid gap-5 sm:grid-cols-2">
        <Field label="Route" hint="model chain">
          <Select
            value={fields.route || 'default'}
            onValueChange={(v) => fields.setRoute(v === 'default' ? '' : v)}
          >
            <SelectTrigger className="mt-1.5 h-10 w-full" aria-label="agent route">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="default">default</SelectItem>
              {routes
                .filter((r) => r.name !== 'default' && r.name !== 'embedding')
                .map((r) => (
                  <SelectItem key={r.name} value={r.name}>
                    {r.name}
                  </SelectItem>
                ))}
            </SelectContent>
          </Select>
        </Field>
        <Field label="Memory">
          <div className="mt-2.5">
            <Toggle on={fields.memory} onChange={fields.setMemory} label="agent memory" />
          </div>
        </Field>
      </div>
      <Field label="Skills allowlist" hint="comma-separated; empty = all">
        <Input
          value={fields.skillsText}
          onChange={(e) => fields.setSkillsText(e.target.value)}
          placeholder="coding-task, research-task"
          className="mt-1.5 h-10"
        />
      </Field>
      <Field label="Tools allowlist" hint="comma-separated; empty = all">
        <Input
          value={fields.toolsText}
          onChange={(e) => fields.setToolsText(e.target.value)}
          placeholder="web_search, web_fetch, shell"
          className="mt-1.5 h-10"
        />
      </Field>
    </div>
  )
}
