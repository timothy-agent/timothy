import { useEffect, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router'
import { getMission, type CreateMissionInput } from '../api/client'
import type { Mission } from '../api/types'
import { MissionForm } from '../components/missions/MissionForm'

// missionToInitial narrows a parent mission down to the fields a
// follow-up seeds (see MissionForm's initial prop) — goal is
// deliberately excluded, left for the user to type.
function missionToInitial(m: Mission): Partial<CreateMissionInput> {
  return {
    kind: m.kind,
    agent_id: m.agent_id,
    repo_url: m.repo_url,
    connector_id: m.connector_id,
    route: m.route,
    review_route: m.review_route,
    plan_route: m.plan_route,
    escalation_route: m.escalation_route,
    harness: m.harness,
    environment: m.environment,
    branch_pattern: m.branch_pattern,
    commit_style: m.commit_style,
    on_complete: m.on_complete,
  }
}

export function NewMission() {
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
  const parentID = searchParams.get('parent') ?? undefined

  // MissionForm's initial prop only ever seeds its useState
  // initializers (they run once) — so a follow-up's form must not
  // render until the parent mission has loaded, rather than seeding
  // in after the fact via a key or an effect.
  const [parent, setParent] = useState<Mission | null>(null)
  const [parentLoading, setParentLoading] = useState(!!parentID)

  useEffect(() => {
    if (!parentID) return
    setParentLoading(true)
    getMission(parentID)
      .then(setParent)
      .catch(() => {
        // Best-effort: an unresolvable parent just falls back to a
        // plain, unseeded create form.
      })
      .finally(() => setParentLoading(false))
  }, [parentID])

  return (
    <div className="mx-auto w-full max-w-full px-8 py-6">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">
          {parentID ? 'Follow-up mission' : 'New mission'}
        </h1>
        <p className="text-sm text-muted-foreground">
          A long-running task that plans, executes, and reviews its own work.
        </p>
      </div>

      <div className="mt-8">
        {parentLoading ? (
          <p className="text-sm text-muted-foreground">Loading parent mission…</p>
        ) : (
          <MissionForm
            mode="create"
            initial={parent ? missionToInitial(parent) : undefined}
            parentMissionId={parent?.id}
            onCancel={() => navigate(-1)}
            onDone={(result) => {
              if (result.kind === 'mission') {
                navigate(`/missions/${result.id}`)
              } else {
                navigate('/automations')
              }
            }}
          />
        )}
      </div>
    </div>
  )
}
