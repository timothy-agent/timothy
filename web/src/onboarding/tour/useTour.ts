import { useCallback, useEffect, useRef, useState } from 'react'
import { useOnboarding } from '../context'
import type { TourDef, TourStep } from './types'

export interface Tour {
  active: boolean
  stepIndex: number
  steps: TourStep[]
  next: () => void
  back: () => void
  skip: () => void
  finish: () => void
  restart: () => void
}

// liveSteps drops steps whose anchor is missing or hidden (a collapsed
// sidebar keeps its items in the DOM with visibility hidden).
function liveSteps(def: TourDef): TourStep[] {
  return def.steps.filter((s) => {
    const el = document.querySelector(`[data-tour="${s.target}"]`)
    return el !== null && (el.checkVisibility?.({ visibilityProperty: true }) ?? true)
  })
}

const restartEvent = 'timothy:tour-restart'

// requestTourRestart starts the named page's tour again if that page is
// mounted with its tour enabled.
export function requestTourRestart(page: string) {
  window.dispatchEvent(new CustomEvent(restartEvent, { detail: page }))
}

function isEditable(el: EventTarget | null): boolean {
  if (!(el instanceof HTMLElement)) return false
  return el.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(el.tagName)
}

// useTour runs a page tour once per version. The page passes enabled
// false while a setup gate shows.
export function useTour(def: TourDef, { enabled }: { enabled: boolean }): Tour {
  const { readiness, progress, updateProgress } = useOnboarding()
  const [active, setActive] = useState(false)
  const [stepIndex, setStepIndex] = useState(0)
  const [steps, setSteps] = useState<TourStep[]>([])
  // Stops a restart while the seen patch is in flight or failed.
  const closed = useRef(false)
  const ready = readiness !== null
  const seen = (progress.tours_seen?.[def.page] ?? 0) >= def.version

  const start = useCallback(() => {
    const live = liveSteps(def)
    if (live.length === 0) return
    setSteps(live)
    setStepIndex(0)
    setActive(true)
  }, [def])

  useEffect(() => {
    if (!enabled || !ready || seen || closed.current) return
    // One frame later, so the page has rendered its anchors.
    const id = requestAnimationFrame(start)
    return () => cancelAnimationFrame(id)
  }, [enabled, ready, seen, start])

  const finish = useCallback(() => {
    closed.current = true
    setActive(false)
    // Best effort: a lost patch only shows the tour again next visit.
    updateProgress({ tours_seen: { [def.page]: def.version } }).catch(() => {})
  }, [def.page, def.version, updateProgress])

  const next = useCallback(() => {
    if (stepIndex >= steps.length - 1) finish()
    else setStepIndex(stepIndex + 1)
  }, [stepIndex, steps.length, finish])

  const back = useCallback(() => setStepIndex((i) => Math.max(0, i - 1)), [])

  const restart = useCallback(() => {
    closed.current = false
    start()
  }, [start])

  useEffect(() => {
    if (!enabled) return
    const onRestart = (e: Event) => {
      if ((e as CustomEvent<string>).detail === def.page) restart()
    }
    window.addEventListener(restartEvent, onRestart)
    return () => window.removeEventListener(restartEvent, onRestart)
  }, [enabled, def.page, restart])

  const shown = active && enabled

  useEffect(() => {
    if (!shown) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault()
        finish()
        return
      }
      if (isEditable(e.target)) return
      if (e.key === 'Enter' || e.key === 'ArrowRight') {
        e.preventDefault()
        next()
      } else if (e.key === 'ArrowLeft') {
        e.preventDefault()
        back()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [shown, next, back, finish])

  return { active: shown, stepIndex, steps, next, back, skip: finish, finish, restart }
}
