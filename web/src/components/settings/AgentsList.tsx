import { Add01Icon, AiBrain01Icon, Delete02Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useCallback, useEffect, useState } from 'react'
import { useNavigate } from 'react-router'
import { toast } from 'sonner'
import { deleteAgent, listAgents, patchAgent, setDefaultAgent } from '../../api/client'
import type { AdminAgent } from '../../api/types'
import { Badge } from '../ui/badge'
import { Button } from '../ui/button'
import { Switch } from '../ui/switch'
import { ConfirmDialog } from '../timothy/confirm-dialog'
import { EmptyState } from '../timothy/empty-state'
import { PageHeader, SectionHeader } from '../timothy/page-header'
import { PageShell } from '../timothy/page-shell'
import { EntityCard } from './EntityCard'
import { settingsArea } from './settingsAreas'
import { errText } from './util'

const area = settingsArea('agents')

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
    <PageShell>
      <PageHeader
        title={area.label}
        description={area.description}
        breadcrumbs={[{ label: 'Settings', href: '/settings' }, { label: area.label }]}
        actions={
          <Button onClick={() => navigate('/settings/agents/new')}>
            <HugeiconsIcon icon={Add01Icon} />
            New agent
          </Button>
        }
      />
      <div className="space-y-10">
        <section className="space-y-4">
          <SectionHeader
            title={agents.length > 0 ? `Agents · ${agents.length}` : 'Agents'}
            description="Who serves a session: a prompt overlay, a model chain (route), skill and tool allowlists, and whether long-term memory participates. The default agent serves new sessions unless the composer picks another."
          />
          {agents.length === 0 ? (
            <EmptyState title="No agents configured yet" description="Add one to serve sessions." />
          ) : (
            <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
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
          )}
        </section>

        <ConfirmDialog
          open={confirmDelete !== null}
          onOpenChange={(o) => !o && setConfirmDelete(null)}
          title={`Delete ${confirmDelete?.name}?`}
          description="Sessions that used this agent keep their history; new turns fall back to the default agent. The default agent itself cannot be deleted."
          confirmLabel="Delete"
          destructive
          onConfirm={() => void remove()}
        />
      </div>
    </PageShell>
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
    <EntityCard
      to={`/settings/agents/${agent.id}`}
      title={agent.name}
      tile={
        <span className="flex size-9 shrink-0 items-center justify-center rounded-md bg-brand-soft text-brand-soft-foreground">
          <HugeiconsIcon icon={AiBrain01Icon} className="size-4.5" />
        </span>
      }
      badges={agent.is_default && <Badge variant="brand">default</Badge>}
      summary={
        agent.description ? (
          <p className="line-clamp-2 text-sm text-muted-foreground">{agent.description}</p>
        ) : undefined
      }
      status={
        <p className="text-xs text-muted-foreground">
          route <span className="font-mono text-foreground">{agent.route || 'default'}</span>
          {' · '}memory {agent.memory ? 'on' : 'off'}
        </p>
      }
      footer={
        <>
          <Switch checked={agent.enabled} onCheckedChange={toggle} aria-label={`${agent.name} enabled`} />
          {!agent.is_default && (
            <Button size="sm" variant="outline" disabled={!agent.enabled} onClick={makeDefault} className="flex-1">
              Make default
            </Button>
          )}
          <Button size="sm" variant="outline" onClick={onManage} className="flex-1">
            Manage
          </Button>
          {!agent.is_default && (
            <Button
              size="icon-sm"
              variant="ghost"
              aria-label={`Delete ${agent.name}`}
              onClick={onDelete}
              className="text-muted-foreground hover:text-destructive"
            >
              <HugeiconsIcon icon={Delete02Icon} className="size-4" />
            </Button>
          )}
        </>
      }
    />
  )
}
