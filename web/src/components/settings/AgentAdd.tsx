import { ArrowLeft01Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { toast } from 'sonner'
import { createAgent, listRoutes } from '../../api/client'
import type { AdminRoute } from '../../api/types'
import { Button } from '../ui/button'
import { AgentForm, slugify, useAgentForm } from './AgentForm'
import { errText } from './util'

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
    <div className="mt-6 w-full space-y-6">
      <Link
        to="/settings/agents"
        className="inline-flex w-fit items-center gap-1.5 text-sm text-muted-foreground transition hover:text-foreground"
      >
        <HugeiconsIcon icon={ArrowLeft01Icon} className="size-4" />
        Agents
      </Link>

      <div className="border-b border-border pb-6">
        <h1 className="text-xl font-semibold tracking-tight">New agent</h1>
        <p className="text-sm text-muted-foreground">
          Who serves a session: prompt, route, skill and tool allowlists, memory.
        </p>
      </div>

      <div className="max-w-3xl">
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
    </div>
  )
}
