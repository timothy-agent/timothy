import { Delete02Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useCallback, useEffect, useState } from 'react'
import {
  createAgent,
  deleteAgent,
  listAgents,
  listRoutes,
  patchAgent,
  setDefaultAgent,
} from '../../api/client'
import type { AdminAgent, AdminRoute } from '../../api/types'
import { Button } from '../ui/button'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '../ui/dialog'
import { Input } from '../ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '../ui/select'
import { ErrorBanner, Field, Toggle } from './shared'
import { errText } from './util'

// slugify mirrors the backend's name rule: lowercase slug.
function slugify(v: string): string {
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

export function AgentsTab() {
  const [agents, setAgents] = useState<AdminAgent[]>([])
  const [routes, setRoutes] = useState<AdminRoute[]>([])
  const [error, setError] = useState<string | null>(null)
  const [adding, setAdding] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState<AdminAgent | null>(null)

  const refresh = useCallback(() => {
    Promise.all([listAgents(), listRoutes()])
      .then(([a, r]) => {
        setAgents(a)
        setRoutes(r)
        setError(null)
      })
      .catch((err: unknown) => setError(errText(err)))
  }, [])
  useEffect(refresh, [refresh])

  const remove = async () => {
    if (!confirmDelete) return
    try {
      await deleteAgent(confirmDelete.id)
      setConfirmDelete(null)
      refresh()
    } catch (err) {
      setError(errText(err))
      setConfirmDelete(null)
    }
  }

  return (
    <div className="mt-6 space-y-4">
      <p className="text-sm text-muted-foreground">
        Agents are who serves a session: a prompt overlay, a model chain (route), skill and tool
        allowlists, and whether long-term memory participates. The default agent serves new
        sessions unless the composer picks another.
      </p>
      <ErrorBanner message={error} />
      {agents.map((a) => (
        <AgentCard
          key={a.id}
          agent={a}
          routes={routes}
          onChanged={refresh}
          onDelete={() => setConfirmDelete(a)}
          onError={setError}
        />
      ))}
      <Button variant="outline" onClick={() => setAdding(true)}>
        + New agent
      </Button>

      {adding && (
        <AgentDialog
          routes={routes}
          onClose={() => setAdding(false)}
          onSaved={() => {
            setAdding(false)
            refresh()
          }}
          onError={setError}
        />
      )}

      <Dialog open={confirmDelete !== null} onOpenChange={(o) => !o && setConfirmDelete(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete {confirmDelete?.name}?</DialogTitle>
          </DialogHeader>
          <p className="text-sm text-muted-foreground">
            Sessions that used this agent keep their history; new turns fall back to the default
            agent. The default agent itself cannot be deleted.
          </p>
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfirmDelete(null)}>
              Cancel
            </Button>
            <Button variant="destructive" onClick={() => void remove()}>
              Delete
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

function AgentCard({
  agent,
  routes,
  onChanged,
  onDelete,
  onError,
}: {
  agent: AdminAgent
  routes: AdminRoute[]
  onChanged: () => void
  onDelete: () => void
  onError: (msg: string) => void
}) {
  const [editing, setEditing] = useState(false)

  const toggle = (enabled: boolean) => {
    patchAgent(agent.id, { enabled }).then(onChanged, (err: unknown) => onError(errText(err)))
  }
  const makeDefault = () => {
    setDefaultAgent(agent.id).then(onChanged, (err: unknown) => onError(errText(err)))
  }

  return (
    <div className="rounded-xl border border-border p-4">
      <div className="flex flex-wrap items-center gap-3">
        <span className="font-medium capitalize">{agent.name}</span>
        {agent.is_default ? (
          <span className="rounded bg-sky-500/15 px-1.5 py-px text-[10px] font-medium uppercase text-sky-600 dark:text-sky-400">
            default
          </span>
        ) : (
          <Button size="sm" variant="outline" disabled={!agent.enabled} onClick={makeDefault}>
            Make default
          </Button>
        )}
        <div className="ml-auto flex items-center gap-3">
          <Button size="sm" variant="outline" onClick={() => setEditing(true)}>
            Edit
          </Button>
          <Toggle on={agent.enabled} onChange={toggle} label={`${agent.name} enabled`} />
          {!agent.is_default && (
            <button
              type="button"
              aria-label={`Delete ${agent.name}`}
              onClick={onDelete}
              className="text-muted-foreground hover:text-red-500"
            >
              <HugeiconsIcon icon={Delete02Icon} className="size-4" />
            </button>
          )}
        </div>
      </div>
      {agent.description && (
        <p className="mt-1 text-xs text-muted-foreground">{agent.description}</p>
      )}
      <p className="mt-1.5 text-xs text-muted-foreground">
        route: <span className="font-mono">{agent.route || 'default'}</span>
        {' · '}skills: {agent.skills.length ? agent.skills.join(', ') : 'all'}
        {' · '}tools: {agent.tools.length ? agent.tools.join(', ') : 'all'}
        {' · '}memory: {agent.memory ? 'on' : 'off'}
      </p>

      {editing && (
        <AgentDialog
          agent={agent}
          routes={routes}
          onClose={() => setEditing(false)}
          onSaved={() => {
            setEditing(false)
            onChanged()
          }}
          onError={onError}
        />
      )}
    </div>
  )
}

// AgentDialog creates or edits one agent. Name is immutable after
// creation (it lives in ledger rows and event payloads).
function AgentDialog({
  agent,
  routes,
  onClose,
  onSaved,
  onError,
}: {
  agent?: AdminAgent
  routes: AdminRoute[]
  onClose: () => void
  onSaved: () => void
  onError: (msg: string) => void
}) {
  const [name, setName] = useState(agent?.name ?? '')
  const [description, setDescription] = useState(agent?.description ?? '')
  const [overlay, setOverlay] = useState(agent?.prompt_overlay ?? '')
  const [route, setRoute] = useState(agent?.route ?? '')
  const [skillsText, setSkillsText] = useState(agent?.skills.join(', ') ?? '')
  const [toolsText, setToolsText] = useState(agent?.tools.join(', ') ?? '')
  const [memory, setMemory] = useState(agent?.memory ?? true)
  const [busy, setBusy] = useState(false)

  const submit = async () => {
    setBusy(true)
    try {
      if (agent) {
        await patchAgent(agent.id, {
          description: description.trim(),
          prompt_overlay: overlay,
          route,
          skills: splitList(skillsText),
          tools: splitList(toolsText),
          memory,
        })
      } else {
        await createAgent({
          name: slugify(name),
          description: description.trim(),
          prompt_overlay: overlay,
          route,
          skills: splitList(skillsText),
          tools: splitList(toolsText),
          memory,
          enabled: true,
        })
      }
      onSaved()
    } catch (err) {
      onError(errText(err))
      onClose()
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && !busy && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{agent ? `Edit ${agent.name}` : 'New agent'}</DialogTitle>
        </DialogHeader>
        <div className="space-y-3">
          {!agent && (
            <Field label="Name (unique slug, immutable)">
              <Input
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="infra, homelab, writer…"
                className="mt-1 h-8"
              />
            </Field>
          )}
          <Field label="Description (shown in the picker)">
            <Input
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="What this agent is for"
              className="mt-1 h-8"
            />
          </Field>
          <Field label="Prompt overlay (appended to the system prompt)">
            <textarea
              value={overlay}
              onChange={(e) => setOverlay(e.target.value)}
              aria-label="Prompt overlay"
              rows={4}
              placeholder="Instructions, persona, house rules…"
              className="mt-1 w-full rounded-lg border border-border bg-transparent p-2 text-sm outline-none focus:border-ring"
            />
          </Field>
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="Route (model chain)">
              <Select value={route || 'default'} onValueChange={(v) => setRoute(v === 'default' ? '' : v)}>
                <SelectTrigger className="mt-1 h-8 w-full text-xs" aria-label="agent route">
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
              <div className="mt-2">
                <Toggle on={memory} onChange={setMemory} label="agent memory" />
              </div>
            </Field>
          </div>
          <Field label="Skills allowlist (comma-separated; empty = all)">
            <Input
              value={skillsText}
              onChange={(e) => setSkillsText(e.target.value)}
              placeholder="coding-task, research-task"
              className="mt-1 h-8"
            />
          </Field>
          <Field label="Tools allowlist (comma-separated; empty = all)">
            <Input
              value={toolsText}
              onChange={(e) => setToolsText(e.target.value)}
              placeholder="web_search, web_fetch, shell"
              className="mt-1 h-8"
            />
          </Field>
        </div>
        <DialogFooter>
          <Button variant="ghost" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button disabled={busy || (!agent && slugify(name) === '')} onClick={() => void submit()}>
            {agent ? 'Save' : 'Create'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
