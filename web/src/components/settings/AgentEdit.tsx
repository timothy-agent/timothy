import { Delete02Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useCallback, useEffect, useState } from 'react'
import { Navigate, useNavigate, useParams } from 'react-router'
import { toast } from 'sonner'
import { deleteAgent, listAgents, listRoutes, patchAgent } from '../../api/client'
import type { AdminAgent, AdminRoute } from '../../api/types'
import { Button } from '../ui/button'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '../ui/dialog'
import { PageHeader } from '../timothy/page-header'
import { PageShell } from '../timothy/page-shell'
import { AgentForm, useAgentForm } from './AgentForm'
import { settingsArea } from './settingsAreas'
import { errText } from './util'

const area = settingsArea('agents')

export function AgentEdit() {
  const { id } = useParams()
  const navigate = useNavigate()
  const [agent, setAgent] = useState<AdminAgent | null | undefined>(undefined)
  const [routes, setRoutes] = useState<AdminRoute[]>([])
  const [busy, setBusy] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState(false)

  const refresh = useCallback(() => {
    Promise.all([listAgents(), listRoutes()])
      .then(([agents, r]) => {
        setAgent(agents.find((a) => a.id === id) ?? null)
        setRoutes(r)
      })
      .catch((err: unknown) => toast.error('Could not load agent', { description: errText(err) }))
  }, [id])
  useEffect(refresh, [refresh])

  // useAgentForm must run unconditionally regardless of load state;
  // it seeds blank until `agent` resolves, which is fine since the
  // form itself doesn't render until then.
  const { value, fields } = useAgentForm(agent ?? undefined)

  if (agent === null) return <Navigate to="/settings/agents" replace />
  if (agent === undefined) return null

  const submit = async () => {
    setBusy(true)
    try {
      await patchAgent(agent.id, {
        description: value.description,
        prompt_overlay: value.overlay,
        route: value.route,
        skills: value.skills,
        tools: value.tools,
        knowledge: value.knowledge,
        memory: value.memory,
        harness: value.harness,
      })
      toast.success('Agent saved')
      navigate('/settings/agents')
    } catch (err) {
      toast.error('Could not save agent', { description: errText(err) })
    } finally {
      setBusy(false)
    }
  }

  const remove = async () => {
    try {
      await deleteAgent(agent.id)
      toast.success('Agent removed', { description: `${agent.name} no longer serves sessions.` })
      navigate('/settings/agents')
    } catch (err) {
      toast.error('Could not remove agent', { description: errText(err) })
      setConfirmDelete(false)
    }
  }

  return (
    <PageShell width="form">
      <PageHeader
        title={agent.name}
        description={agent.is_default ? 'Default agent' : 'Agent'}
        breadcrumbs={[
          { label: 'Settings', href: '/settings' },
          { label: area.label, href: '/settings/agents' },
          { label: agent.name },
        ]}
        actions={
          !agent.is_default && (
            <Button variant="destructive" onClick={() => setConfirmDelete(true)}>
              <HugeiconsIcon icon={Delete02Icon} />
              Delete
            </Button>
          )
        }
      />

      <div>
        <AgentForm isNew={false} routes={routes} fields={fields} />

        <div className="flex gap-3 pt-6">
          <Button variant="outline" disabled={busy} onClick={() => navigate('/settings/agents')}>
            Cancel
          </Button>
          <Button disabled={busy} onClick={() => void submit()}>
            Save
          </Button>
        </div>
      </div>

      <Dialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete {agent.name}?</DialogTitle>
          </DialogHeader>
          <p className="text-sm text-muted-foreground">
            Sessions that used this agent keep their history; new turns fall back to the default
            agent.
          </p>
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfirmDelete(false)}>
              Cancel
            </Button>
            <Button variant="destructive" onClick={() => void remove()}>
              Delete
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </PageShell>
  )
}
