import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useLocation } from 'react-router'
import { errorText, getOnboarding, getToken, patchOnboarding, subscribeTokenChanged } from '../api/client'
import type { OnboardingProgress, OnboardingProgressPatch, Readiness } from '../api/types'
import { OnboardingContext } from './context'

export function OnboardingProvider({ children }: { children: React.ReactNode }) {
  const { pathname } = useLocation()
  const [readiness, setReadiness] = useState<Readiness | null>(null)
  const [progress, setProgress] = useState<OnboardingProgress>({})
  const [loading, setLoading] = useState(() => getToken() !== '')
  const [error, setError] = useState<string | null>(null)
  const inFlight = useRef(false)

  // No token yet: skip, a 401 would reopen the token dialog in a loop.
  // loading covers the first fetch only; later ones refresh in place.
  const refresh = useCallback(async () => {
    if (inFlight.current || !getToken()) return
    inFlight.current = true
    try {
      const o = await getOnboarding()
      setReadiness(o.readiness)
      setProgress(o.progress)
      setError(null)
    } catch (err) {
      setError(errorText(err))
    } finally {
      inFlight.current = false
      setLoading(false)
    }
  }, [])

  const updateProgress = useCallback(async (patch: OnboardingProgressPatch) => {
    setProgress(await patchOnboarding(patch))
  }, [])

  useEffect(() => {
    void refresh()
  }, [pathname, refresh])

  useEffect(() => {
    const onFocus = () => void refresh()
    window.addEventListener('focus', onFocus)
    return () => window.removeEventListener('focus', onFocus)
  }, [refresh])

  useEffect(() => subscribeTokenChanged(() => void refresh()), [refresh])

  const value = useMemo(
    () => ({ readiness, progress, loading, error, refresh, updateProgress }),
    [readiness, progress, loading, error, refresh, updateProgress],
  )
  return <OnboardingContext.Provider value={value}>{children}</OnboardingContext.Provider>
}
