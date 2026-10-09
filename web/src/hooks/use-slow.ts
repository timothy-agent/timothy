import { useEffect, useState } from 'react'

// useSlow turns true once busy has stayed true for delayMs.
export function useSlow(busy: boolean, delayMs = 2000) {
  const [slow, setSlow] = useState(false)

  useEffect(() => {
    if (!busy) return
    const timer = window.setTimeout(() => setSlow(true), delayMs)
    return () => {
      window.clearTimeout(timer)
      setSlow(false)
    }
  }, [busy, delayMs])

  return busy && slow
}
