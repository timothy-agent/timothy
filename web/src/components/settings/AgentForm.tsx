import { useEffect, useState } from 'react'
import { Input } from '../ui/input'
import { Textarea } from '../ui/textarea'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '../ui/select'
import { Field, Toggle } from './shared'
import { KnowledgePicker } from './KnowledgePicker'
import { SkillsPicker } from './SkillsPicker'
import { ToolsPicker } from './ToolsPicker'
import type { AdminAgent, AdminRoute } from '../../api/types'

// slugify mirrors the backend's name rule: lowercase slug.
export function slugify(v: string): string {
  return v
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
}

export interface AgentFormValue {
  name: string
  description: string
  overlay: string
  route: string
  skills: string[]
  tools: string[]
  knowledge: string[]
  memory: boolean
}

export function useAgentForm(agent?: AdminAgent) {
  const [name, setName] = useState(agent?.name ?? '')
  const [description, setDescription] = useState(agent?.description ?? '')
  const [overlay, setOverlay] = useState(agent?.prompt_overlay ?? '')
  const [route, setRoute] = useState(agent?.route ?? '')
  const [skills, setSkills] = useState<string[]>(agent?.skills ?? [])
  const [tools, setTools] = useState<string[]>(agent?.tools ?? [])
  const [knowledge, setKnowledge] = useState<string[]>(agent?.knowledge ?? [])
  const [memory, setMemory] = useState(agent?.memory ?? true)

  // Edit loads its agent asynchronously, after this hook has already
  // mounted with blank defaults — useState's initializer only runs
  // once, so the fields never pick up the fetched agent without this.
  useEffect(() => {
    if (!agent) return
    setName(agent.name)
    setDescription(agent.description)
    setOverlay(agent.prompt_overlay)
    setRoute(agent.route)
    setSkills(agent.skills)
    setTools(agent.tools)
    setKnowledge(agent.knowledge ?? [])
    setMemory(agent.memory)
  }, [agent])

  const value: AgentFormValue = {
    name,
    description: description.trim(),
    overlay,
    route,
    skills,
    tools,
    knowledge,
    memory,
  }

  return {
    value,
    canSubmit: agent ? true : slugify(name) !== '',
    fields: {
      name,
      setName,
      description,
      setDescription,
      overlay,
      setOverlay,
      route,
      setRoute,
      skills,
      setSkills,
      tools,
      setTools,
      knowledge,
      setKnowledge,
      memory,
      setMemory,
    },
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
        <Textarea
          value={fields.overlay}
          onChange={(e) => fields.setOverlay(e.target.value)}
          aria-label="Prompt overlay"
          rows={5}
          placeholder="Instructions, persona, house rules… Markdown supported."
          className="mt-1.5 min-h-32 resize-y text-sm"
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
      <Field label="Skills allowlist" hint="pick from the loaded skill packs; empty = none">
        <SkillsPicker value={fields.skills} onChange={fields.setSkills} />
      </Field>
      <Field label="Tools allowlist" hint="pick from the live tool surface; empty = none">
        <ToolsPicker value={fields.tools} onChange={fields.setTools} />
      </Field>
      <Field label="Knowledge allowlist" hint="collections this agent can search with kb_search; empty = none">
        <KnowledgePicker value={fields.knowledge} onChange={fields.setKnowledge} />
      </Field>
    </div>
  )
}
