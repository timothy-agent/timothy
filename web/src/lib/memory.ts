import { useEffect, useState } from 'react'
import { listMemories } from '../api/client'

// memoryChangedEvent links the Memory page to the sidebar badge
// without a shared provider: queue actions dispatch, the badge
// listens.
export const memoryChangedEvent = 'timothy:memory-changed'

export function notifyMemoryChanged() {
  window.dispatchEvent(new Event(memoryChangedEvent))
}

// usePendingMemories keeps badges current: initial fetch, a slow
// poll, and an instant refresh when the Memory page acts.
export function usePendingMemories(): number {
  const [count, setCount] = useState(0)
  useEffect(() => {
    let live = true
    const refresh = () => {
      listMemories('pending')
        .then((pending) => live && setCount(pending.length))
        .catch(() => {})
    }
    refresh()
    const timer = setInterval(refresh, 60_000)
    window.addEventListener(memoryChangedEvent, refresh)
    return () => {
      live = false
      clearInterval(timer)
      window.removeEventListener(memoryChangedEvent, refresh)
    }
  }, [])
  return count
}
