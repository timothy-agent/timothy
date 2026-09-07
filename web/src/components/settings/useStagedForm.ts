import { useCallback, useRef, useState } from 'react'

// useStagedForm holds a configuration form's fields locally until Save
// (contract 10.7): setField marks a field touched, dirty is a deep
// per-field comparison against the current baseline, reset discards
// back to the baseline (Cancel), and rebase applies a refetched
// baseline to untouched fields only, keeping touched ones (contract
// 10.7 "Baseline resync").
export function useStagedForm<T extends object>(baseline: T) {
  const [values, setValues] = useState<T>(baseline)
  const [touched, setTouched] = useState<Set<keyof T>>(new Set())
  const baselineRef = useRef(baseline)
  baselineRef.current = baseline

  const setField = useCallback(<K extends keyof T>(key: K, value: T[K]) => {
    setValues((prev) => ({ ...prev, [key]: value }))
    setTouched((prev) => {
      const next = new Set(prev)
      next.add(key)
      return next
    })
  }, [])

  const reset = useCallback(() => {
    setValues(baselineRef.current)
    setTouched(new Set())
  }, [])

  const rebase = useCallback((next: T) => {
    setValues((prev) => {
      const merged = { ...next }
      for (const key of touched) {
        merged[key] = prev[key]
      }
      return merged
    })
  }, [touched])

  const dirtyFields = [...touched].filter(
    (key) => !deepEqual(values[key], baseline[key]),
  ) as (keyof T)[]
  const dirty = dirtyFields.length > 0

  return { values, setField, dirty, dirtyFields, reset, rebase }
}

function deepEqual(a: unknown, b: unknown): boolean {
  if (a === b) return true
  if (typeof a !== typeof b) return false
  if (Array.isArray(a) && Array.isArray(b)) {
    return a.length === b.length && a.every((v, i) => deepEqual(v, b[i]))
  }
  if (a && b && typeof a === 'object' && typeof b === 'object') {
    const aKeys = Object.keys(a as object)
    const bKeys = Object.keys(b as object)
    if (aKeys.length !== bKeys.length) return false
    return aKeys.every((k) => deepEqual((a as Record<string, unknown>)[k], (b as Record<string, unknown>)[k]))
  }
  return false
}
