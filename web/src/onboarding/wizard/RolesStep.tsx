import { useEffect, useState } from 'react'
import { Link } from 'react-router'
import { listRoutes } from '../../api/client'
import type { AdminRoute } from '../../api/types'
import { Button } from '../../components/ui/button'
import { errText } from '../../lib/errors'

// roleLines turns the four role assignments into plain sentences.
const roleLines: { role: string; with: (model: string) => string; none: string }[] = [
  { role: 'default', with: (m) => `Chat answers with ${m}`, none: 'Chat: no model yet' },
  { role: 'summarize', with: (m) => `Summaries use ${m}`, none: 'Summaries: no model yet' },
  { role: 'embedding', with: (m) => `Memory and knowledge search use ${m}`, none: 'Memory and knowledge search: no model yet' },
  { role: 'vision', with: (m) => `Images: ${m}`, none: 'Images: no model yet, add a provider with a vision model later' },
]

function modelFor(routes: AdminRoute[], role: string): string | null {
  const r = routes.find((x) => x.role === role)
  const model = r?.serving?.model ?? r?.chain[0]?.model
  if (!model) return null
  const provider = r?.resolved?.[0]?.provider_name
  return provider ? `${model} from ${provider}` : model
}

export function RolesStep({ onBack, onNext }: { onBack: () => void; onNext: () => void }) {
  const [routes, setRoutes] = useState<AdminRoute[] | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let active = true
    listRoutes().then(
      (rs) => {
        if (active) setRoutes(rs)
      },
      (err: unknown) => {
        if (active) setError(errText(err))
      },
    )
    return () => {
      active = false
    }
  }, [])

  return (
    <div>
      <h1 className="text-xl font-semibold">What Timothy uses</h1>
      <div className="mt-6 space-y-2 text-sm">
        {error !== null && <p className="text-destructive">{error}</p>}
        {routes === null && error === null && <p className="text-muted-foreground">Loading...</p>}
        {routes !== null &&
          roleLines.map((l) => {
            const model = modelFor(routes, l.role)
            return <p key={l.role}>{model ? l.with(model) : l.none}</p>
          })}
      </div>
      <p className="mt-4 text-sm">
        <Link to="/settings/routes" className="font-medium text-primary underline underline-offset-2 hover:no-underline">
          Change in Settings
        </Link>
      </p>
      <div className="mt-8 flex gap-2">
        <Button variant="outline" onClick={onBack}>
          Back
        </Button>
        <Button onClick={onNext}>Continue</Button>
      </div>
    </div>
  )
}
