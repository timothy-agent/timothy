import { createContext, useContext } from 'react'
import type { OnboardingProgress, OnboardingProgressPatch, Readiness } from '../api/types'

// OnboardingState is live setup readiness plus the stored progress.
// readiness stays null until the first fetch lands, so gates never
// flash while loading.
export interface OnboardingState {
  readiness: Readiness | null
  progress: OnboardingProgress
  loading: boolean
  error: string | null
  refresh: () => Promise<void>
  updateProgress: (patch: OnboardingProgressPatch) => Promise<void>
}

export const OnboardingContext = createContext<OnboardingState | null>(null)

export function useOnboarding(): OnboardingState {
  const ctx = useContext(OnboardingContext)
  if (!ctx) throw new Error('useOnboarding must be used inside OnboardingProvider')
  return ctx
}
