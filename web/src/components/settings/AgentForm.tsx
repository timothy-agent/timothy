import { useState } from 'react'
import { Input } from '../ui/input'
import { Textarea } from '../ui/textarea'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '../ui/select'
import { Switch } from '../ui/switch'
import { Field, FieldGroup } from '../timothy/field'
import { UNSET } from './util'
import { AllowlistPicker } from './AllowlistPicker'
import { EXECUTOR_DEFAULT, executorChoices } from '../missions/MissionForm'
import { listKbCollections, listSkills, listTools } from '../../api/client'
import { useStagedForm } from './useStagedForm'
import type { AdminAgent, AdminRoute } from '../../api/types'

interface AgentFormValue {
  name: string
  description: string
  overlay: string
  route: string
  skills: string[]
  tools: string[]
  knowledge: string[]
  memory: boolean
  harness: string
}

export function useAgentForm() {
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [overlay, setOverlay] = useState('')
  const [route, setRoute] = useState('')
  const [skills, setSkills] = useState<string[]>([])
  const [tools, setTools] = useState<string[]>([])
  const [knowledge, setKnowledge] = useState<string[]>([])
  const [memory, setMemory] = useState(true)
  const [harness, setHarness] = useState('')

  const value: AgentFormValue = {
    name,
    description: description.trim(),
    overlay,
    route,
    skills,
    tools,
    knowledge,
    memory,
    harness,
  }

  return {
    value,
    canSubmit: name.trim() !== '',
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
      harness,
      setHarness,
    },
  }
}

export type AgentStagedValue = AgentFormValue

function baselineFrom(agent: AdminAgent): AgentStagedValue {
  return {
    name: agent.name,
    description: agent.description,
    overlay: agent.prompt_overlay,
    route: agent.route,
    skills: agent.skills,
    tools: agent.tools,
    knowledge: agent.knowledge ?? [],
    memory: agent.memory,
    harness: agent.harness ?? '',
  }
}

// useAgentEditForm stages AgentEdit's fields (contract 10.7): nothing
// commits until Save, Cancel discards back to the loaded agent, and a
// sibling refresh (after a successful Save) rebases untouched fields
// while keeping ones the user is still editing. Same `fields` shape as
// useAgentForm so AgentForm renders either without knowing which.
export function useAgentEditForm(agent: AdminAgent) {
  const staged = useStagedForm<AgentStagedValue>(baselineFrom(agent))

  return {
    dirty: staged.dirty,
    reset: staged.reset,
    rebase: (next: AdminAgent) => staged.rebase(baselineFrom(next)),
    value: staged.values,
    fields: {
      name: staged.values.name,
      setName: (v: string) => staged.setField('name', v),
      description: staged.values.description,
      setDescription: (v: string) => staged.setField('description', v),
      overlay: staged.values.overlay,
      setOverlay: (v: string) => staged.setField('overlay', v),
      route: staged.values.route,
      setRoute: (v: string) => staged.setField('route', v),
      skills: staged.values.skills,
      setSkills: (v: string[]) => staged.setField('skills', v),
      tools: staged.values.tools,
      setTools: (v: string[]) => staged.setField('tools', v),
      knowledge: staged.values.knowledge,
      setKnowledge: (v: string[]) => staged.setField('knowledge', v),
      memory: staged.values.memory,
      setMemory: (v: boolean) => staged.setField('memory', v),
      harness: staged.values.harness,
      setHarness: (v: string) => staged.setField('harness', v),
    },
  }
}

// AgentForm renders the shared field set for both create and edit:
// name is plain text, editable on both pages (the uuid is the stable
// key, not the name).
export function AgentForm({
  routes,
  fields,
}: {
  routes: AdminRoute[]
  fields: ReturnType<typeof useAgentForm>['fields'] | ReturnType<typeof useAgentEditForm>['fields']
}) {
  return (
    <FieldGroup>
      <Field label="Name" description="unique, case-insensitive">
        <Input
          value={fields.name}
          onChange={(e) => fields.setName(e.target.value)}
          placeholder="Infra, Homelab, Writer…"
        />
      </Field>
      <Field label="Description" description="shown in the picker">
        <Input
          value={fields.description}
          onChange={(e) => fields.setDescription(e.target.value)}
          placeholder="What this agent is for"
        />
      </Field>
      <Field label="Prompt overlay" description="appended to the system prompt">
        <Textarea
          value={fields.overlay}
          onChange={(e) => fields.setOverlay(e.target.value)}
          aria-label="Prompt overlay"
          rows={5}
          placeholder="Instructions, persona, house rules… Markdown supported."
          className="min-h-32 resize-y text-sm"
        />
      </Field>
      <div className="grid gap-5 sm:grid-cols-2">
        <Field label="Route" description="model chain">
          {(props) => (
            <Select
              value={fields.route || UNSET}
              onValueChange={(v) => fields.setRoute(v === UNSET ? '' : v)}
            >
              <SelectTrigger id={props.id} className="w-full" aria-label="agent route">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={UNSET}>default</SelectItem>
                {routes
                  .filter((r) => r.name !== 'default' && r.name !== 'embedding')
                  .map((r) => (
                    <SelectItem key={r.name} value={r.name}>
                      {r.name}
                    </SelectItem>
                  ))}
              </SelectContent>
            </Select>
          )}
        </Field>
        <Field label="Memory">
          {(props) => (
            <div className="flex h-9 items-center">
              <Switch id={props.id} checked={fields.memory} onCheckedChange={fields.setMemory} aria-label="agent memory" />
            </div>
          )}
        </Field>
      </div>
      <Field label="Harness" description="coding executor this agent's missions delegate to; inherit falls through to settings">
        {(props) => (
          <Select
            value={fields.harness || EXECUTOR_DEFAULT}
            onValueChange={(v) => fields.setHarness(v === EXECUTOR_DEFAULT ? '' : v)}
          >
            <SelectTrigger id={props.id} className="w-full" aria-label="agent harness">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {executorChoices.map((c) => (
                <SelectItem key={c.value} value={c.value}>
                  {c.value === EXECUTOR_DEFAULT ? 'Inherit from settings' : c.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
      </Field>
      <AllowlistPicker
        label="Skills allowlist"
        description="pick from the loaded skill packs; empty = none"
        value={fields.skills}
        onChange={fields.setSkills}
        load={async () =>
          (await listSkills()).map((s) => ({ id: s.name, label: s.name, description: s.description }))
        }
        cacheKey="skills"
        emptyText="No skill matches."
        freeTextPlaceholder="research-brief, coding"
      />
      <AllowlistPicker
        label="Tools allowlist"
        description="pick from the live tool surface; empty = none"
        value={fields.tools}
        onChange={fields.setTools}
        load={async () =>
          (await listTools()).map((t) => ({ id: t.name, label: t.name, description: t.description }))
        }
        cacheKey="tools"
        emptyText="No tool matches."
        freeTextPlaceholder="search_web, fetch_url, shell"
      />
      <AllowlistPicker
        label="Knowledge allowlist"
        description="search_kb always searches the whole knowledge base; these collections rank higher in results"
        value={fields.knowledge}
        onChange={fields.setKnowledge}
        load={async () =>
          (await listKbCollections()).map((c) => ({ id: c.name, label: c.name, description: c.description }))
        }
        cacheKey="knowledge"
        emptyText="No collection matches."
        freeTextPlaceholder="product-docs, runbooks"
      />
    </FieldGroup>
  )
}
