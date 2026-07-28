import { useNavigate } from 'react-router'
import { MissionForm } from '../components/missions/MissionForm'

export function NewMission() {
  const navigate = useNavigate()

  return (
    <div className="mx-auto w-full max-w-3xl px-4 py-6">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">New mission</h1>
        <p className="text-sm text-muted-foreground">
          A long-running task that plans, executes, and reviews its own work.
        </p>
      </div>

      <div className="mt-8">
        <MissionForm
          mode="create"
          onCancel={() => navigate(-1)}
          onDone={(result) => {
            if (result.kind === 'mission') {
              navigate(`/missions/${result.id}`)
            } else {
              navigate('/missions')
            }
          }}
        />
      </div>
    </div>
  )
}
