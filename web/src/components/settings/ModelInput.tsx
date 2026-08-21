import { useEffect, useMemo, useRef, useState } from 'react'
import type { CatalogModel } from '../../api/types'
import { Input } from '../ui/input'
import { Popover, PopoverContent, PopoverAnchor } from '../ui/popover'

const catalogSearchDebounceMs = 250

// useCatalogSearch is a live type-ahead over the synced model catalog:
// debounces q (250ms) before calling search, so a request fires per
// pause in typing rather than per keystroke, and guards against
// out-of-order responses with a request-sequence counter — a stale
// response (fired before the latest q but resolving after it) is
// dropped rather than clobbering what's now on screen. Every
// ModelInput host (ModelsSection/CliModelsSection, ProviderAdd,
// RouteEdit's chain entry picker) shares this so the guard lives once.
// search must be stable across renders that don't change its own
// inputs (wrap with useCallback) or it re-debounces on every keystroke
// for no reason.
export function useCatalogSearch(
  q: string,
  search: (q: string) => Promise<CatalogModel[]>,
): CatalogModel[] {
  const [models, setModels] = useState<CatalogModel[]>([])
  const seq = useRef(0)

  useEffect(() => {
    const mySeq = ++seq.current
    const t = setTimeout(() => {
      search(q).then(
        (result) => {
          if (mySeq === seq.current) setModels(result)
        },
        () => {
          if (mySeq === seq.current) setModels([])
        },
      )
    }, catalogSearchDebounceMs)
    return () => clearTimeout(t)
  }, [q, search])

  return models
}

export interface ModelSuggestion {
  id: string
  name?: string
  input_per_mtok?: number
  output_per_mtok?: number
}

// formatMtokPrice renders one per-Mtok price, USD, trimming trailing
// zeros ($3, not $3.00) but keeping meaningful decimals ($0.15).
function formatMtokPrice(v: number): string {
  const fixed = v.toFixed(2)
  return `$${fixed.endsWith('.00') ? fixed.slice(0, -3) : fixed}`
}

// priceLabel renders the row's price as "in $X · out $Y /MTok",
// labeled so it's clear which number is which side. undefined means
// unknown (never guessed, D-013); 0 is a real price — the catalog's
// *float64 fields preserve nil-vs-0 end to end, so an explicit 0 (a
// genuinely free model, e.g. z.ai's flash tier) renders as "$0", not
// as unknown. "unpriced" when both sides are undefined; "free" when
// both are present and 0; otherwise a known side renders its price and
// an unknown side renders "N/A" (same missing-number convention as
// PipelineCard).
export function priceLabel(s: ModelSuggestion): string {
  const { input_per_mtok: in_, output_per_mtok: out } = s
  if (in_ == null && out == null) return 'unpriced'
  if (in_ === 0 && out === 0) return 'free'
  const label = (v: number | undefined) => (v == null ? 'N/A' : formatMtokPrice(v))
  return `in ${label(in_)} · out ${label(out)} /MTok`
}

// configuredPrice picks a declared model's own prices, unless both
// input and output are zero. Unlike the catalog (Entry's *float64
// fields preserve nil-vs-0), a provider row's ModelPrices are plain
// (non-optional) floats — an unset field and a genuinely-free model
// both serialize as 0, so there's no way to tell them apart here. Cost
// honesty means treating that ambiguous all-zero case as "never
// priced" rather than guessing "free". Shared by every declared-model
// suggestion builder (ProviderAdd, RouteEdit's chain entry picker) so
// the fallback rule lives once.
export function configuredPrice(
  prices: { input_per_mtok?: number; output_per_mtok?: number } | undefined,
): Pick<ModelSuggestion, 'input_per_mtok' | 'output_per_mtok'> | undefined {
  const { input_per_mtok, output_per_mtok } = prices ?? {}
  if (!input_per_mtok && !output_per_mtok) return undefined
  return { input_per_mtok, output_per_mtok }
}

// catalogMatchForID mirrors the gateway's catalog.Match rule (internal
// /gateway/catalog/match.go): exact model_key first, else the first
// catalog row whose key's last "/"-segment equals id. Catalog keys are
// often provider-prefixed (xai/grok-2, zai/glm-4.7-flash), so a
// declared id like grok-2 needs the segment fallback to find its
// price — an exact-only lookup leaves it unpriced despite a known
// price being in the pool. Shared by every declared-model suggestion
// builder that falls back to a catalog price.
export function catalogMatchForID(id: string, pool: CatalogModel[]): CatalogModel | undefined {
  const exact = pool.find((c) => c.model_key === id)
  if (exact) return exact
  return pool.find((c) => {
    const i = c.model_key.lastIndexOf('/')
    return (i >= 0 ? c.model_key.slice(i + 1) : c.model_key) === id
  })
}

// catalogRowID is the id a catalog row should display and commit: the
// server-stripped id (the provider's own accepted model id), falling
// back to model_key only if a row somehow arrives without one.
export function catalogRowID(m: CatalogModel): string {
  return m.id || m.model_key
}

// ModelInput is a free-text field with advisory autocomplete: it never
// blocks an unlisted id (models change too often to gate on a fixed
// catalog), it just surfaces ones we already have real evidence for —
// a preset's validated default, or ids declared on other configured
// providers of the same driver.
export function ModelInput({
  value,
  onChange,
  onCommit,
  suggestions,
  placeholder,
  className,
  id,
  ariaLabel,
}: {
  value: string
  onChange: (v: string) => void
  // onCommit, when given, fires once the value is "final": on blur (with
  // whatever was last typed) and when a suggestion is picked (with that
  // suggestion's id) — not on every keystroke like onChange. For a
  // save-on-blur field (see ProviderEdit's CLI default model picker).
  onCommit?: (v: string) => void
  suggestions: ModelSuggestion[]
  placeholder?: string
  className?: string
  id?: string
  // ariaLabel, for a field whose own <Field label> wraps more than one
  // control (see RouteEdit's provider+model row) and so can't give this
  // input its accessible name on its own.
  ariaLabel?: string
}) {
  const [open, setOpen] = useState(false)
  const skipNextOpen = useRef(false)
  const inputRef = useRef<HTMLInputElement>(null)

  const filtered = useMemo(() => {
    const q = value.trim().toLowerCase()
    const list = q
      ? suggestions.filter(
          (s) => s.id.toLowerCase().includes(q) || s.name?.toLowerCase().includes(q),
        )
      : suggestions
    return list.slice(0, 8)
  }, [suggestions, value])

  return (
    <Popover open={open && filtered.length > 0} onOpenChange={setOpen}>
      <PopoverAnchor asChild>
        <Input
          ref={inputRef}
          id={id}
          aria-label={ariaLabel}
          value={value}
          onChange={(e) => {
            onChange(e.target.value)
            setOpen(true)
          }}
          onFocus={() => {
            if (!skipNextOpen.current) setOpen(true)
            skipNextOpen.current = false
          }}
          onKeyDown={(e) => {
            if (e.key === 'Escape') setOpen(false)
          }}
          onBlur={() => onCommit?.(value)}
          placeholder={placeholder}
          className={className}
          autoComplete="off"
        />
      </PopoverAnchor>
      <PopoverContent
        align="start"
        className="w-max min-w-(--radix-popover-trigger-width) max-w-[min(32rem,90vw)] p-1"
        onOpenAutoFocus={(e) => e.preventDefault()}
        // The anchor is a plain input focused by the same click that
        // opens this popover — Radix's outside-click detection can
        // otherwise see that first click as "outside" (content isn't
        // mounted yet when the pointerdown fires) and close it on the
        // same interaction that opened it.
        onInteractOutside={(e) => {
          if (e.target === inputRef.current) e.preventDefault()
        }}
      >
        <ul role="listbox" className="max-h-56 overflow-y-auto">
          {filtered.map((s) => (
            <li key={s.id}>
              <button
                type="button"
                role="option"
                aria-selected={s.id === value}
                // Blur fires on mousedown, before this click handler —
                // without preventDefault here, onCommit would fire twice:
                // once from onBlur with the stale typed value, then again
                // from onClick with the picked id.
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => {
                  onChange(s.id)
                  onCommit?.(s.id)
                  skipNextOpen.current = true
                  setOpen(false)
                }}
                className="flex w-full items-center justify-between gap-3 rounded-md px-2.5 py-1.5 text-left text-sm hover:bg-muted"
              >
                <span className="min-w-0 whitespace-nowrap">
                  {s.name ?? s.id}
                  {s.name && (
                    <span className="ml-1.5 font-mono text-xs text-muted-foreground">
                      {s.id}
                    </span>
                  )}
                </span>
                <span className="ml-auto shrink-0 whitespace-nowrap font-mono text-xs text-muted-foreground">
                  {priceLabel(s)}
                </span>
              </button>
            </li>
          ))}
        </ul>
      </PopoverContent>
    </Popover>
  )
}
