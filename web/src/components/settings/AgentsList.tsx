import { Add01Icon, AiBrain01Icon, Delete02Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useCallback, useEffect, useState } from 'react'
import { useNavigate } from 'react-router'
import { toast } from 'sonner'
import { deleteAgent, listAgents, patchAgent, setDefaultAgent } from '../../api/client'
import type { AdminAgent } from '../../api/types'
import { Button } from '../ui/button'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '../ui/dialog'
import { Toggle } from './shared'
import { errText } from './util'

export function AgentsList() {
  const [agents, setAgents] = useState<AdminAgent[]>([])
  const [confirmDelete, setConfirmDelete] = useState<AdminAgent | null>(null)
  const navigate = useNavigate()

  const refresh = useCallback(() => {
    listAgents()
      .then(setAgents)
      .catch((err: unknown) => toast.error('Could not load agents', { description: errText(err) }))
  }, [])
  useEffect(refresh, [refresh])

  const remove = async () => {
    if (!confirmDelete) return
    try {
      await deleteAgent(confirmDelete.id)
      toast.success('Agent removed', { description: `${confirmDelete.name} no longer serves sessions.` })
      setConfirmDelete(null)
      refresh()
    } catch (err) {
      toast.error('Could not remove agent', { description: errText(err) })
      setConfirmDelete(null)
    }
  }

  return (
    <div className="mt-6 space-y-6">
      <p className="text-sm text-muted-foreground">
        Agents are who serves a session: a prompt overlay, a model chain (route), skill and tool
        allowlists, and whether long-term memory participates. The default agent serves new
        sessions unless the composer picks another.
      </p>

      <div className="flex items-center justify-between">
        <h2 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
          Agents · {agents.length}
        </h2>
        <Button onClick={() => navigate('/settings/agents/new')}>
          <HugeiconsIcon icon={Add01Icon} />
          New agent
        </Button>
      </div>

      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {agents.map((a) => (
          <AgentCard
            key={a.id}
            agent={a}
            onChanged={refresh}
            onManage={() => navigate(`/settings/agents/${a.id}`)}
            onDelete={() => setConfirmDelete(a)}
          />
        ))}
      </div>

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
  onChanged,
  onManage,
  onDelete,
}: {
  agent: AdminAgent
  onChanged: () => void
  onManage: () => void
  onDelete: () => void
}) {
  const toggle = (enabled: boolean) => {
    patchAgent(agent.id, { enabled }).then(onChanged, (err: unknown) =>
      toast.error('Could not update agent', { description: errText(err) }),
    )
  }
  const makeDefault = () => {
    setDefaultAgent(agent.id).then(onChanged, (err: unknown) =>
      toast.error('Could not set default agent', { description: errText(err) }),
    )
  }

  return (
    <div className="flex flex-col gap-3 rounded-xl border border-border bg-card p-4 shadow-sm transition hover:shadow-md">
      <div className="flex items-center gap-3">
        <span className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-brand-soft text-brand-soft-foreground">
          <HugeiconsIcon icon={AiBrain01Icon} className="size-4.5" />
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <span className="truncate text-sm font-semibold capitalize">{agent.name}</span>
            {agent.is_default && (
              <span className="rounded bg-brand-soft px-1.5 py-0.5 text-xs font-semibold text-brand-soft-foreground">
                default
              </span>
            )}
          </div>
        </div>
        <Toggle on={agent.enabled} onChange={toggle} label={`${agent.name} enabled`} />
      </div>

      {agent.description && (
        <p className="line-clamp-2 text-sm text-muted-foreground">{agent.description}</p>
      )}

      <p className="text-xs text-muted-foreground">
        route <span className="font-mono text-foreground">{agent.route || 'default'}</span>
        {' · '}memory {agent.memory ? 'on' : 'off'}
      </p>

      <div className="mt-auto flex items-center gap-2 pt-1">
        {!agent.is_default && (
          <Button size="sm" variant="outline" disabled={!agent.enabled} onClick={makeDefault} className="flex-1">
            Make default
          </Button>
        )}
        <Button size="sm" variant="outline" onClick={onManage} className="flex-1">
          Manage
        </Button>
        {!agent.is_default && (
          <button
            type="button"
            aria-label={`Delete ${agent.name}`}
            onClick={onDelete}
            className="text-muted-foreground hover:text-destructive"
          >
            <HugeiconsIcon icon={Delete02Icon} className="size-4" />
          </button>
        )}
      </div>
    </div>
  )
}
