import {
  ArrowLeft01Icon,
  ArrowRight01Icon,
  Delete02Icon,
} from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import type { AdminProvider, RouteEntryStatus } from '../../../api/types'
import { matchPreset } from '../presets'
import { ProviderLogo } from '../ProviderLogo'
import { ScoreBar } from './ScoreBar'

const fmtLatency = (v?: number) => (v === undefined ? 'N/A' : `${Math.round(v)} ms`)
// Trims binary-float noise (e.g. 0.39999999999999997) before display.
const fmtMTok = (v: number) => `$${+v.toFixed(2)}`
const fmtPrice = (input?: number, output?: number) => {
  if (input === undefined && output === undefined) return 'unpriced'
  if (input === undefined) return `out ${fmtMTok(output as number)}`
  if (output === undefined) return `in ${fmtMTok(input)}`
  return `${fmtMTok(input)} / ${fmtMTok(output)}`
}
const fmtUptime = (v?: number) => (v === undefined ? 'N/A' : `${Math.round(v * 100)}%`)

export function PipelineCard({
  provider,
  name,
  model,
  status,
  serving,
  scored,
  maxScore,
  index,
  count,
  onMove,
  onRemove,
  dragging,
}: {
  provider?: AdminProvider
  name: string
  model: string
  status?: RouteEntryStatus
  serving: boolean
  scored: boolean
  maxScore: number
  index: number
  count: number
  onMove: (from: number, to: number) => void
  onRemove: () => void
  dragging?: boolean
}) {
  const usable = status ? status.usable : true
  const priceTitle =
    status?.input_per_mtok !== undefined || status?.output_per_mtok !== undefined
      ? `in $${status?.input_per_mtok ?? '?'} / out $${status?.output_per_mtok ?? '?'} per MTok`
      : undefined
  return (
    <div
      data-testid="pipeline-card"
      className={`w-full select-none space-y-2.5 rounded-xl border border-border bg-card p-4 shadow-sm transition ${
        dragging ? 'opacity-60 ring-2 ring-brand' : ''
      } ${scored ? '' : 'cursor-grab'}`}
    >
      <div className="flex items-center gap-2.5">
        <span className="shrink-0 font-mono text-[10px] text-muted-foreground">#{index + 1}</span>
        {provider && <ProviderLogo preset={matchPreset(provider)} className="size-7" />}
        <span className="min-w-0 flex-1 truncate text-sm font-semibold">{name}</span>
        <span
          data-testid="health-dot"
          title={status?.skip_reason}
          className={`size-1.5 shrink-0 rounded-full ${usable ? 'bg-good' : 'bg-warning'}`}
        />
      </div>
      <p className="truncate font-mono text-xs text-muted-foreground" title={model}>
        {model || '(default model)'}
      </p>
      <dl className="grid grid-cols-3 gap-1 text-[10px] text-muted-foreground">
        <div>
          <dt>latency</dt>
          <dd className="font-mono text-foreground">{fmtLatency(status?.latency_ms)}</dd>
        </div>
        <div className="min-w-0">
          <dt>price /MTok</dt>
          <dd className="truncate font-mono text-foreground" title={priceTitle}>
            {fmtPrice(status?.input_per_mtok, status?.output_per_mtok)}
          </dd>
        </div>
        <div>
          <dt>uptime</dt>
          <dd className="font-mono text-foreground">{fmtUptime(status?.uptime)}</dd>
        </div>
      </dl>
      {scored && <ScoreBar score={status?.score} max={maxScore} />}
      <div className="flex items-center gap-1">
        {serving && (
          <span className="rounded-4xl bg-good-soft px-2 py-0.5 text-[10px] font-medium text-good">
            serving
          </span>
        )}
        {!usable && status?.skip_reason && (
          <span className="truncate text-[10px] text-warning" title={status.skip_reason}>
            {status.skip_reason}
          </span>
        )}
        <span className="ml-auto flex items-center gap-1">
          {!scored && (
            <>
              <button
                type="button"
                aria-label={`Move ${model} left`}
                disabled={index === 0}
                onClick={() => onMove(index, index - 1)}
                className="text-muted-foreground hover:text-foreground disabled:opacity-30"
              >
                <HugeiconsIcon icon={ArrowLeft01Icon} className="size-4" />
              </button>
              <button
                type="button"
                aria-label={`Move ${model} right`}
                disabled={index === count - 1}
                onClick={() => onMove(index, index + 1)}
                className="text-muted-foreground hover:text-foreground disabled:opacity-30"
              >
                <HugeiconsIcon icon={ArrowRight01Icon} className="size-4" />
              </button>
            </>
          )}
          <button
            type="button"
            aria-label={`Remove ${model}`}
            onClick={onRemove}
            className="text-muted-foreground hover:text-destructive"
          >
            <HugeiconsIcon icon={Delete02Icon} className="size-4" />
          </button>
        </span>
      </div>
    </div>
  )
}
