import { useEffect, useId, useMemo, useRef, useState } from 'react'
import { Button } from '../../components/ui/button'
import { Popover, PopoverAnchor, PopoverContent } from '../../components/ui/popover'
import type { Tour } from './useTour'

const pad = 6
const radius = 8

// maskPath covers the viewport with a rounded hole around rect.
function maskPath(rect: DOMRect, vw: number, vh: number): string {
  const x = rect.left - pad
  const y = rect.top - pad
  const w = rect.width + pad * 2
  const h = rect.height + pad * 2
  const r = Math.min(radius, w / 2, h / 2)
  return [
    `M0 0H${vw}V${vh}H0Z`,
    `M${x + r} ${y}H${x + w - r}A${r} ${r} 0 0 1 ${x + w} ${y + r}`,
    `V${y + h - r}A${r} ${r} 0 0 1 ${x + w - r} ${y + h}`,
    `H${x + r}A${r} ${r} 0 0 1 ${x} ${y + h - r}`,
    `V${y + r}A${r} ${r} 0 0 1 ${x + r} ${y}Z`,
  ].join('')
}

export function TourOverlay({ active, stepIndex, steps, next, back, skip }: Tour) {
  const step = active ? steps[stepIndex] : undefined
  // key ties a measurement to its step, so a stale one never renders.
  const [placed, setPlaced] = useState<{ key: string; el: Element; rect: DOMRect } | null>(null)
  const nextRef = useRef<HTMLButtonElement>(null)
  const titleId = useId()
  const target = placed?.el ?? null
  const anchorRef = useMemo(() => ({ current: target }), [target])

  useEffect(() => {
    const el = step ? document.querySelector(`[data-tour="${step.target}"]`) : null
    if (!step || !el) return
    el.scrollIntoView?.({ block: 'center' })
    const measure = () => setPlaced({ key: step.target, el, rect: el.getBoundingClientRect() })
    const id = requestAnimationFrame(measure)
    window.addEventListener('resize', measure)
    window.addEventListener('scroll', measure, true)
    return () => {
      cancelAnimationFrame(id)
      window.removeEventListener('resize', measure)
      window.removeEventListener('scroll', measure, true)
    }
  }, [step])

  if (!step || !placed || placed.key !== step.target) return null
  const { rect } = placed
  const last = stepIndex === steps.length - 1

  return (
    <>
      <svg
        data-testid="tour-mask"
        aria-hidden
        className="pointer-events-none fixed inset-0 z-40 size-full"
      >
        <path
          d={maskPath(rect, window.innerWidth, window.innerHeight)}
          fillRule="evenodd"
          className="pointer-events-auto fill-foreground/50"
          onClick={(e) => e.stopPropagation()}
        />
      </svg>
      <div
        aria-hidden
        className="pointer-events-none fixed z-40 rounded-md outline outline-2 outline-brand"
        style={{ left: rect.left, top: rect.top, width: rect.width, height: rect.height }}
      />
      <Popover open>
        <PopoverAnchor virtualRef={anchorRef} />
        <PopoverContent
          side={step.side ?? 'bottom'}
          className="z-50 w-80"
          role="dialog"
          aria-labelledby={titleId}
          onOpenAutoFocus={(e) => {
            e.preventDefault()
            nextRef.current?.focus()
          }}
          onInteractOutside={(e) => e.preventDefault()}
          onEscapeKeyDown={(e) => e.preventDefault()}
        >
          <p id={titleId} className="font-semibold">
            {step.title}
          </p>
          <p className="text-muted-foreground">{step.body}</p>
          <div className="flex items-center gap-1.5 pt-1">
            <span className="mr-auto text-xs text-muted-foreground tabular-nums">
              Step {stepIndex + 1} of {steps.length}
            </span>
            {stepIndex > 0 && (
              <Button variant="ghost" size="sm" onClick={back}>
                Back
              </Button>
            )}
            <Button variant="ghost" size="sm" onClick={skip}>
              Skip
            </Button>
            <Button ref={nextRef} size="sm" onClick={next}>
              {last ? 'Done' : 'Next'}
            </Button>
          </div>
        </PopoverContent>
      </Popover>
    </>
  )
}
