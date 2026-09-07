import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router'
import { toast } from 'sonner'
import { createAgent, listRoutes } from '../../api/client'
import type { AdminRoute } from '../../api/types'
import { Button } from '../ui/button'
import { PageHeader } from '../timothy/page-header'
import { PageShell } from '../timothy/page-shell'
import { AgentForm, useAgentForm } from './AgentForm'
import { settingsArea } from './settingsAreas'
import { errText, slugify } from './util'

const area = settingsArea('agents')

export function AgentAdd() {
  const navigate = useNavigate()
  const [routes, setRoutes] = useState<AdminRoute[]>([])
  const [busy, setBusy] = useState(false)
  const { value, canSubmit, fields } = useAgentForm()

  useEffect(() => {
    listRoutes().then(setRoutes, () => undefined)
  }, [])

  const submit = async () => {
    setBusy(true)
    try {
      await createAgent({
        name: slugify(value.name),
        description: value.description,
        prompt_overlay: value.overlay,
        route: value.route,
        skills: value.skills,
        tools: value.tools,
        knowledge: value.knowledge,
        memory: value.memory,
        harness: value.harness,
        enabled: true,
      })
      toast.success('Agent created', { description: `${slugify(value.name)} is ready to serve sessions.` })
      navigate('/settings/agents')
    } catch (err) {
      toast.error('Could not create agent', { description: errText(err) })
    } finally {
      setBusy(false)
    }
  }

  return (
    <PageShell width="form">
      <PageHeader
        title="New agent"
        description="Who serves a session: prompt, route, skill and tool allowlists, memory."
        breadcrumbs={[
          { label: 'Settings', href: '/settings' },
          { label: area.label, href: '/settings/agents' },
          { label: 'New agent' },
        ]}
      />

      <div>
        <AgentForm isNew routes={routes} fields={fields} />

        <div className="flex gap-3 pt-6">
          <Button variant="outline" disabled={busy} onClick={() => navigate('/settings/agents')}>
            Cancel
          </Button>
          <Button disabled={!canSubmit || busy} onClick={() => void submit()}>
            Create agent
          </Button>
        </div>
      </div>
    </PageShell>
  )
}
