import { Circle, CircleCheck } from 'lucide-react'
import { useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { toast } from 'sonner'
import { createMission } from '../api/client'
import { Panel } from '../components/timothy/panel'
import { Button } from '../components/ui/button'
import { Progress } from '../components/ui/progress'
import { errText } from '../lib/errors'
import { cn } from '../lib/utils'
import { buildChecklist, canDismiss, checklistProgress } from './checklist'
import { useOnboarding } from './context'
import { sampleMissionInput } from './sampleMission'

export function SetupChecklist() {
  const { readiness, progress, updateProgress } = useOnboarding()
  const navigate = useNavigate()
  const [starting, setStarting] = useState(false)
  if (!readiness || progress.checklist_dismissed === true) return null

  const items = buildChecklist(readiness, progress)
  const { done, total, percent } = checklistProgress(items)
  const sampleReady = readiness.chat_route && readiness.sandbox

  const dismiss = () => {
    updateProgress({ checklist_dismissed: true }).catch((err: unknown) =>
      toast.error('Could not hide the checklist', { description: errText(err) }),
    )
  }

  const runSample = async () => {
    setStarting(true)
    try {
      const mission = await createMission(sampleMissionInput())
      navigate(`/missions/${mission.id}`)
    } catch (err) {
      toast.error('Could not start the sample mission', { description: errText(err) })
      setStarting(false)
    }
  }

  return (
    <Panel
      title="Set up Timothy"
      description={`${done} of ${total} done`}
      actions={
        canDismiss(items) && (
          <Button variant="ghost" size="sm" onClick={dismiss}>
            Dismiss
          </Button>
        )
      }
    >
      <Progress value={percent} aria-label="Setup progress" />
      <ul className="mt-4 flex flex-col divide-y divide-border">
        {items.map((item) => (
          <li key={item.key} className="flex items-start gap-3 py-3">
            {item.done ? (
              <CircleCheck aria-hidden="true" className="mt-0.5 size-4 shrink-0 fill-brand text-brand-foreground" />
            ) : (
              <Circle aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
            )}
            <div className="min-w-0 flex-1">
              <p className={cn('text-sm font-medium', item.done && 'text-muted-foreground line-through')}>
                <span className="sr-only">{item.done ? 'Done: ' : 'To do: '}</span>
                {item.title}
              </p>
              <p className="text-xs text-muted-foreground">{item.why}</p>
            </div>
            {!item.done && (
              <div className="flex shrink-0 flex-wrap justify-end gap-2">
                {item.key === 'first_mission' && sampleReady && (
                  <Button variant="outline" size="xs" disabled={starting} onClick={() => void runSample()}>
                    Run a sample mission
                  </Button>
                )}
                <Button asChild variant="ghost" size="xs">
                  <Link to={item.to} aria-label={`Open: ${item.title}`}>
                    Open
                  </Link>
                </Button>
              </div>
            )}
          </li>
        ))}
      </ul>
    </Panel>
  )
}
