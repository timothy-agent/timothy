import { useCallback, useEffect, useRef, useState } from 'react'

// targetIndex maps a pointer x to a destination position among the
// cards whose horizontal midpoints are given in display order: the
// number of midpoints left of the pointer, clamped to a valid index.
export function targetIndex(midpoints: number[], x: number): number {
  if (midpoints.length === 0) return 0
  let idx = 0
  for (const m of midpoints) {
    if (x > m) idx++
  }
  return Math.min(idx, midpoints.length - 1)
}

// reorder moves one element of a list, returning a new array.
export function reorder<T>(list: T[], from: number, to: number): T[] {
  const next = [...list]
  const [item] = next.splice(from, 1)
  next.splice(to, 0, item)
  return next
}

const activationPx = 5

// useReorderDrag is a dependency-free pointer-event drag for
// horizontally laid-out cards. The caller registers each card element
// via setItemRef and spreads handleProps(i) on its drag surface;
// dragIndex/overIndex drive the live preview and onCommit fires once
// on drop. Escape or pointercancel aborts.
export function useReorderDrag({
  disabled,
  onCommit,
}: {
  disabled?: boolean
  onCommit: (from: number, to: number) => void
}) {
  const [dragIndex, setDragIndex] = useState<number | null>(null)
  const [overIndex, setOverIndex] = useState<number | null>(null)
  const items = useRef<(HTMLElement | null)[]>([])
  const gesture = useRef<{ from: number; startX: number; active: boolean } | null>(null)

  const setItemRef = useCallback(
    (i: number) => (el: HTMLElement | null) => {
      items.current[i] = el
    },
    [],
  )

  const stop = useCallback(() => {
    gesture.current = null
    setDragIndex(null)
    setOverIndex(null)
  }, [])

  useEffect(() => {
    const onMove = (e: PointerEvent) => {
      const g = gesture.current
      if (!g) return
      if (!g.active) {
        if (Math.abs(e.clientX - g.startX) < activationPx) return
        g.active = true
        setDragIndex(g.from)
      }
      const midpoints = items.current
        .filter((el): el is HTMLElement => el !== null)
        .map((el) => {
          const r = el.getBoundingClientRect()
          return r.left + r.width / 2
        })
      setOverIndex(targetIndex(midpoints, e.clientX))
    }
    const onUp = () => {
      const g = gesture.current
      if (!g) return
      if (g.active) {
        // Read the latest preview position from state via the setter to
        // avoid a stale closure over overIndex.
        setOverIndex((over) => {
          if (over !== null && over !== g.from) onCommit(g.from, over)
          return null
        })
      }
      gesture.current = null
      setDragIndex(null)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') stop()
    }
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
    window.addEventListener('pointercancel', stop)
    window.addEventListener('keydown', onKey)
    return () => {
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)
      window.removeEventListener('pointercancel', stop)
      window.removeEventListener('keydown', onKey)
    }
  }, [onCommit, stop])

  const handleProps = (i: number) => ({
    onPointerDown: (e: React.PointerEvent<HTMLElement>) => {
      if (disabled || e.button !== 0) return
      // Buttons inside the card keep their own click behavior.
      if ((e.target as HTMLElement).closest('button')) return
      gesture.current = { from: i, startX: e.clientX, active: false }
    },
  })

  return { dragIndex, overIndex, handleProps, setItemRef }
}
