import { useEffect, useRef } from 'react'
import { getToken } from '../api/client'
import { useOnboarding } from './context'

export type VisitedKey = 'memory' | 'automations' | 'analytics'

// useMarkVisited records that the operator opened a page, once.
export function useMarkVisited(key: VisitedKey) {
  const { progress, updateProgress } = useOnboarding()
  const sent = useRef(false)
  const seen = progress.visited?.includes(key) ?? false

  useEffect(() => {
    if (sent.current || seen || !getToken()) return
    sent.current = true
    // Best effort: a lost mark only leaves the checklist item open.
    updateProgress({ visited: [key] }).catch(() => {})
  }, [key, seen, updateProgress])
}
