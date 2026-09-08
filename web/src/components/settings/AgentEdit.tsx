import { Trash2 } from 'lucide-react'
import { useCallback, useEffect, useState } from 'react'
import { Navigate, useNavigate, useParams } from 'react-router'
import { toast } from 'sonner'
import { deleteAgent, listAgents, listRoutes, patchAgent } from '../../api/client'
import type { AdminAgent, AdminRoute } from '../../api/types'
import { Button } from '../ui/button'
import { Alert, AlertDescription } from '../ui/alert'
import { ConfirmDialog } from '../timothy/confirm-dialog'
import { Form, FormActions } from '../timothy/field'
import { PageHeader } from '../timothy/page-header'
import { PageShell } from '../timothy/page-shell'
import { AgentForm, useAgentEditForm } from './AgentForm'
import { settingsArea } from './settingsAreas'
import { errText } from '../../lib/errors'

const area = settingsArea('agents')

export function AgentEdit() {
  const { id } = useParams()
  const [agent, setAgent] = useState<AdminAgent | null | undefined>(undefined)
  const [routes, setRoutes] = useState<AdminRoute[]>([])

  const refresh = useCallback(() => {
    return Promise.all([listAgents(), listRoutes()])
      .then(([agents, r]) => {
        const found = agents.find((a) => a.id === id) ?? null
        setAgent(found)
        setRoutes(r)
        return found
      })
      .catch((err: unknown) => {
        toast.error('Could not load agent', { description: errText(err) })
        return undefined
      })
  }, [id])
  useEffect(() => {
    void refresh()
  }, [refresh])

  if (agent === null) return <Navigate to="/settings/agents" replace />
  if (agent === undefined) return null

  return <AgentEditForm key={agent.id} initialAgent={agent} routes={routes} refresh={refresh} />
}

function AgentEditForm({
  initialAgent,
  routes,
  refresh,
}: {
  initialAgent: AdminAgent
  routes: AdminRoute[]
  refresh: () => Promise<AdminAgent | null | undefined>
}) {
  const navigate = useNavigate()
  const [agent, setAgentState] = useState(initialAgent)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)

  const staged = useAgentEditForm(initialAgent)

  const save = useCallback(async () => {
    setSaving(true)
    setSaveError(null)
    try {
      await patchAgent(agent.id, {
        name: staged.value.name.trim(),
        description: staged.value.description,
        prompt_overlay: staged.value.overlay,
        route: staged.value.route,
        skills: staged.value.skills,
        tools: staged.value.tools,
        knowledge: staged.value.knowledge,
        memory: staged.value.memory,
        harness: staged.value.harness,
      })
      toast.success('Agent saved')
      const refetched = await refresh()
      if (refetched) {
        setAgentState(refetched)
        staged.rebase(refetched)
      }
    } catch (err) {
      setSaveError(errText(err))
    } finally {
      setSaving(false)
    }
  }, [agent.id, refresh, staged])

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
              <Trash2 aria-hidden />
              Delete
            </Button>
          )
        }
      />

      <Form
        onSubmit={(e) => {
          e.preventDefault()
          void save()
        }}
      >
        <AgentForm routes={routes} fields={staged.fields} />

        {saveError && (
          <Alert tone="destructive">
            <AlertDescription className="flex flex-wrap items-center justify-between gap-3">
              <span>Could not save agent changes: {saveError}</span>
              <Button type="button" size="sm" variant="outline" onClick={() => void save()}>
                Retry
              </Button>
            </AlertDescription>
          </Alert>
        )}

        <FormActions note={staged.dirty ? 'Unsaved changes' : undefined}>
          <Button type="button" variant="outline" disabled={saving} onClick={staged.reset}>
            Cancel
          </Button>
          <Button type="submit" disabled={!staged.dirty || saving}>
            Save
          </Button>
        </FormActions>
      </Form>

      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title={`Delete ${agent.name}?`}
        description="Sessions that used this agent keep their history; new turns fall back to the default agent."
        confirmLabel="Delete"
        destructive
        onConfirm={() => void remove()}
      />
    </PageShell>
  )
}
