import { ArrowLeft } from 'lucide-react'
import { useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { getAutomation } from '../api/client'
import type { Automation } from '../api/types'
import { MissionForm } from '../components/missions/MissionForm'

// EditAutomation loads one automation and edits it through MissionForm.
export function EditAutomation() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const [automation, setAutomation] = useState<Automation | null>(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    if (!id) return
    getAutomation(id).then(
      (a) => {
        setAutomation(a)
        setLoading(false)
      },
      () => setLoading(false),
    )
  }, [id])

  if (!id) return null

  if (loading) {
    return (
      <div className="mx-auto w-full max-w-full px-8 py-6">
        <p className="text-sm text-muted-foreground">Loading…</p>
      </div>
    )
  }

  if (!automation) {
    return (
      <div className="mx-auto w-full max-w-full px-8 py-6">
        <p className="text-sm text-muted-foreground">
          Automation not found.{' '}
          <Link to="/automations" className="underline underline-offset-2 hover:text-foreground">
            Back to automations
          </Link>
        </p>
      </div>
    )
  }

  return (
    <div className="mx-auto w-full max-w-full px-8 py-6">
      <Link
        to={`/automations/${id}`}
        className="inline-flex w-fit items-center gap-1.5 text-sm text-muted-foreground transition hover:text-foreground"
      >
        <ArrowLeft className="size-4" />
        Automation
      </Link>

      <div className="mt-6">
        <h1 className="text-xl font-semibold tracking-tight">Edit automation</h1>
        <p className="text-sm text-muted-foreground">
          A recurring mission that runs on the cron below.
        </p>
      </div>

      <div className="mt-8">
        <MissionForm
          mode="edit"
          automation={automation}
          onCancel={() => navigate(`/automations/${id}`)}
          onDone={() => navigate(`/automations/${id}`)}
        />
      </div>
    </div>
  )
}
