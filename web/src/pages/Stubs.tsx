import { PagePlaceholder } from '../components/PagePlaceholder'

// One stub per future surface, named for the phase that fills it.

export function Lanes() {
  return (
    <PagePlaceholder
      title="Lanes"
      description="Long-running tasks with live status, plans, diffs, and review findings. Arrives with the agent harness."
    />
  )
}

export function Library() {
  return (
    <PagePlaceholder
      title="Library"
      description="Generated images, video, and audio — filterable by type, prompt, and session. Arrives with media generation."
    />
  )
}

export function Queues() {
  return (
    <PagePlaceholder
      title="Queues"
      description="Pending memory confirmations and permission requests. Arrives with tools and memory."
    />
  )
}

