import { useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { listSchedules } from '../api/client'
import type { Schedule } from '../api/types'
import { MissionForm } from '../components/missions/MissionForm'

// No GET-by-id for schedules; the list is small, so find the one
// being edited rather than adding a single-row endpoint (same pattern
// as MissionDetail's schedule strip).
export function EditSchedule() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const [schedule, setSchedule] = useState<Schedule | null>(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    if (!id) return
    listSchedules().then(
      (rows) => {
        setSchedule(rows.find((s) => s.id === id) ?? null)
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

  if (!schedule) {
    return (
      <div className="mx-auto w-full max-w-full px-8 py-6">
        <p className="text-sm text-muted-foreground">
          Schedule not found.{' '}
          <Link to="/automations" className="underline underline-offset-2 hover:text-foreground">
            Back to automations
          </Link>
        </p>
      </div>
    )
  }

  return (
    <div className="mx-auto w-full max-w-full px-8 py-6">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">Edit schedule</h1>
        <p className="text-sm text-muted-foreground">
          A recurring mission that fires on the cron below.
        </p>
      </div>

      <div className="mt-8">
        <MissionForm
          mode="edit"
          schedule={schedule}
          onCancel={() => navigate(`/automations/${id}`)}
          onDone={() => navigate(`/automations/${id}`)}
        />
      </div>
    </div>
  )
}
