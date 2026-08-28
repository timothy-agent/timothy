import { useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router'
import { EChart } from '../components/charts/EChart'
import { StatsLegend } from '../components/charts/StatsLegend'
import {
  areaLinesOption,
  donutOption,
  gaugeOption,
  latencyBarsOption,
  multiLineOption,
  requestsErrorsOption,
  stackedBarsOption,
} from '../components/charts/options'
import { palette } from '../components/charts/palette'
import {
  catalogPrices,
  usageBudget,
  usageCache,
  usageLatency,
  usageSeries,
  usageSessions,
  usageSummary,
  usageTotals,
  usageUnpriced,
} from '../api/client'
import type {
  BudgetStatus,
  CacheRow,
  CatalogPrice,
  GroupTotal,
  LatencyRow,
  SessionUsage,
  UnpricedGroup,
  UsagePoint,
  UsageSummary,
} from '../api/types'
import { estimateUnpriced, totalEstimate } from '../lib/costEstimate'
import { compact, formatDuration, money } from '../lib/format'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '../components/ui/tooltip'

const ranges = [
  { key: 'today', label: 'Today', days: 0, bucket: 'hour' as const },
  { key: '7d', label: '7 days', days: 7, bucket: 'day' as const },
  { key: '30d', label: '30 days', days: 30, bucket: 'day' as const },
  { key: '90d', label: '90 days', days: 90, bucket: 'week' as const },
]

function rangeDates(key: string): { from: Date; to: Date; bucket: 'hour' | 'day' | 'week' } {
  const r = ranges.find((x) => x.key === key) ?? ranges[2]
  const to = new Date()
  const from = new Date()
  if (r.days === 0) from.setHours(0, 0, 0, 0)
  else from.setDate(from.getDate() - r.days)
  return { from, to, bucket: r.bucket }
}

// ConvertedRow is the shape brain's usage decorator adds to a
// {cost|amount, currency} row: converted_amount/currency, present only
// when a stored fx rate exists (never a guess) and it differs from the
// row's own currency.
interface ConvertedRow {
  currency: string
  converted_amount?: number
  converted_currency?: string
}

// primaryMoney renders the user's default_currency figure as the
// headline number when the server could convert this row (a stored fx
// rate exists), falling back to the row's own billing currency
// otherwise — never blocking on a rate that doesn't exist yet.
function primaryMoney(row: ConvertedRow, amount: number): string {
  if (row.converted_amount != null && row.converted_currency) {
    return money(row.converted_amount, row.converted_currency)
  }
  return money(amount, row.currency)
}

// secondaryMoney is the original billing-currency amount, shown muted
// next to a converted primary figure — omitted when nothing converted
// (the primary IS the original amount already).
function secondaryMoney(row: ConvertedRow, amount: number): string | undefined {
  if (row.converted_amount == null || !row.converted_currency) return undefined
  return money(amount, row.currency)
}

interface Loaded {
  // One summary row per billing currency present in the range — never
  // summed together. The header tiles show the dominant (first) row;
  // any additional currencies get their own note below the tiles.
  summaries: UsageSummary[]
  byProvider: UsagePoint[]
  byModel: UsagePoint[]
  providerTotals: GroupTotal[]
  modelTotals: GroupTotal[]
  sessions: SessionUsage[]
  latency: LatencyRow[]
  cache: CacheRow[]
  budget: BudgetStatus | null
}

// sortGroupsByTotal orders group names by their summed value across
// rows, descending, ties broken alphabetically — stack order, legend
// rows, and palette index all follow since they index into this order.
export function sortGroupsByTotal(groups: string[], rows: Record<string, number | string>[]): string[] {
  const totals = new Map(groups.map((g) => [g, 0]))
  for (const row of rows) {
    for (const g of groups) totals.set(g, (totals.get(g) ?? 0) + (Number(row[g]) || 0))
  }
  return [...groups].sort((a, b) => (totals.get(b) ?? 0) - (totals.get(a) ?? 0) || a.localeCompare(b))
}

// pivot turns (bucket, group) points into chart-ready rows keyed by
// bucket with one column per group, groups ordered by total value
// descending. onlyGroups, when given, restricts both the emitted
// columns and the rows' contents to that set — how zero-cost groups
// get excluded from cost charts without touching the token charts fed
// by the same series.
function pivot(points: UsagePoint[], metric: (p: UsagePoint) => number, onlyGroups?: Set<string>) {
  const allGroups = [...new Set(points.map((p) => p.group))]
  const groups = onlyGroups ? allGroups.filter((g) => onlyGroups.has(g)) : allGroups
  const rows = new Map<string, Record<string, number | string>>()
  for (const p of points) {
    if (onlyGroups && !onlyGroups.has(p.group)) continue
    const key = p.bucket
    const row = rows.get(key) ?? { bucket: key }
    row[p.group] = ((row[p.group] as number) ?? 0) + metric(p)
    rows.set(key, row)
  }
  const rowList = [...rows.values()]
  return { groups: sortGroupsByTotal(groups, rowList), rows: rowList }
}

// TotalsRow is one group's collapsed totals — extends ConvertedRow so
// primaryMoney/secondaryMoney work on it directly.
interface TotalsRow extends ConvertedRow {
  group: string
  cost: number
  requests: number
  errors: number
  tokens: number
}

// totals collapses a series into one row per group for the tables.
// Grouped by (group, currency) so a group's cost is never summed
// across billing currencies — in practice this is almost always one
// currency, but the split stays correct if that ever changes.
// converted_amount is summed the same way: every point in range was
// decorated against the same target currency and rate, so summing the
// per-bucket converted figures is exactly as safe as summing cost.
function totals(points: UsagePoint[]): TotalsRow[] {
  const acc = new Map<string, TotalsRow>()
  for (const p of points) {
    const key = `${p.group} ${p.currency}`
    const t = acc.get(key) ?? {
      group: p.group, currency: p.currency, cost: 0, requests: 0, errors: 0, tokens: 0,
      converted_amount: p.converted_amount != null ? 0 : undefined, converted_currency: p.converted_currency,
    }
    t.cost += p.cost
    t.requests += p.requests
    t.errors += p.errors
    t.tokens += p.input_tokens + p.output_tokens
    if (t.converted_amount != null && p.converted_amount != null) t.converted_amount += p.converted_amount
    acc.set(key, t)
  }
  return [...acc.values()].sort((a, b) => b.cost - a.cost)
}

const bucketLabel = (iso: string, bucket: string) => {
  const d = new Date(iso)
  return bucket === 'hour'
    ? d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
    : d.toLocaleDateString([], { month: 'short', day: 'numeric' })
}

// ChartView is the bars/lines toggle shared by the two cost-over-time
// panels — a compact segmented control matching the range picker's style.
type ChartView = 'bars' | 'lines'

function ViewToggle({ view, onChange }: { view: ChartView; onChange: (v: ChartView) => void }) {
  const options: { key: ChartView; label: string }[] = [
    { key: 'bars', label: 'Bars' },
    { key: 'lines', label: 'Lines' },
  ]
  return (
    <div className="flex rounded-lg border border-border p-0.5">
      {options.map((o) => (
        <button
          key={o.key}
          type="button"
          onClick={() => onChange(o.key)}
          className={
            view === o.key
              ? 'rounded-md bg-zinc-200/80 px-2 py-0.5 text-xs font-medium dark:bg-zinc-800'
              : 'rounded-md px-2 py-0.5 text-xs text-muted-foreground hover:text-foreground'
          }
        >
          {o.label}
        </button>
      ))}
    </div>
  )
}

// useSeriesSelection backs a chart's selectable legend with Grafana
// semantics: a plain click isolates that key (hides all others, or
// restores all if it's already the only one visible); an additive
// click (ctrl/cmd) toggles just that key in/out of the visible set.
function useSeriesSelection(keys: string[]) {
  const [hidden, setHidden] = useState<Set<string>>(new Set())
  const onSelect = (key: string, additive: boolean) => {
    setHidden((prev) => {
      if (additive) {
        const next = new Set(prev)
        if (next.has(key)) next.delete(key)
        else next.add(key)
        return next
      }
      const isolated = new Set(keys.filter((k) => k !== key))
      const alreadyIsolated = keys.every((k) => (k === key ? !prev.has(k) : prev.has(k)))
      return alreadyIsolated ? new Set() : isolated
    })
  }
  return { hidden, onSelect }
}

export function Analytics() {
  const [range, setRange] = useState('today')
  const [data, setData] = useState<Loaded | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [unpriced, setUnpriced] = useState<UnpricedGroup[]>([])
  const [prices, setPrices] = useState<CatalogPrice[]>([])

  useEffect(() => {
    const { from, to, bucket } = rangeDates(range)

    let live = true
    setError(null)
    Promise.allSettled([
      usageSummary(from, to),
      usageSeries(from, to, bucket, 'provider'),
      usageSeries(from, to, bucket, 'model'),
      usageTotals(from, to, 'provider'),
      usageTotals(from, to, 'model'),
      usageSessions(from, to, 10),
      usageLatency(from, to),
      usageCache(from, to),
      usageBudget(),
      usageUnpriced(from, to),
    ]).then((results) => {
      if (!live) return
      const failed = results.filter((r) => r.status === 'rejected').length
      setError(failed > 0 ? `${failed} of ${results.length} widgets failed to load` : null)
      function val<T>(r: PromiseSettledResult<T>, fallback: T): T {
        return r.status === 'fulfilled' ? r.value : fallback
      }
      setData({
        summaries: val(results[0], []),
        byProvider: val(results[1], []),
        byModel: val(results[2], []),
        providerTotals: val(results[3], []),
        modelTotals: val(results[4], []),
        sessions: val(results[5], []),
        latency: val(results[6], []),
        cache: val(results[7], []),
        budget: val<BudgetStatus | null>(results[8], null),
      })
      setUnpriced(val<UnpricedGroup[]>(results[9], []))
    })
    return () => {
      live = false
    }
  }, [range])

  const { bucket } = rangeDates(range)

  // Zero-cost groups (unpriced NULL rows and genuinely-free providers
  // like local Ollama) add no signal to a cost view — excluded from
  // the provider/model cost tables and bar charts, but never from
  // token charts fed by the same series.
  const pricedProviders = useMemo(
    () => new Set((data?.providerTotals ?? []).filter((g) => g.cost > 0).map((g) => g.group)),
    [data],
  )
  const pricedModels = useMemo(
    () => new Set((data?.modelTotals ?? []).filter((g) => g.cost > 0).map((g) => g.group)),
    [data],
  )

  // Charts plot the user's default_currency figure when the server
  // could convert a point (converted_amount present), the raw billing
  // amount otherwise — a point in a foreign currency with no stored
  // rate falls back to its recorded amount rather than vanishing.
  const chartCost = (p: UsagePoint) => p.converted_amount ?? p.cost
  const chartCurrency = useMemo(() => {
    for (const p of data?.byProvider ?? []) {
      if (p.converted_currency) return p.converted_currency
    }
    return data?.byProvider.find((p) => p.cost > 0)?.currency ?? 'USD'
  }, [data])
  const chartMoney = (v: number) => money(v, chartCurrency)

  const cost = useMemo(
    () => (data ? pivot(data.byProvider, chartCost, pricedProviders) : { groups: [], rows: [] }),
    [data, pricedProviders],
  )
  const tokens = useMemo(() => {
    if (!data) return []
    const byBucket = new Map<string, { bucket: string; input: number; output: number }>()
    for (const p of data.byProvider) {
      const row = byBucket.get(p.bucket) ?? { bucket: p.bucket, input: 0, output: 0 }
      row.input += p.input_tokens
      row.output += p.output_tokens
      byBucket.set(p.bucket, row)
    }
    return [...byBucket.values()]
  }, [data])

  // requestsErrors sums requests/errors across providers per bucket —
  // the fixed two-series panel (requests as bars, error rate as a line).
  const requestsErrors = useMemo(() => {
    if (!data) return []
    const byBucket = new Map<string, { bucket: string; requests: number; errors: number }>()
    for (const p of data.byProvider) {
      const row = byBucket.get(p.bucket) ?? { bucket: p.bucket, requests: 0, errors: 0 }
      row.requests += p.requests
      row.errors += p.errors
      byBucket.set(p.bucket, row)
    }
    return [...byBucket.values()]
  }, [data])

  const modelCost = useMemo(
    () => (data ? pivot(data.byModel, chartCost, pricedModels) : { groups: [], rows: [] }),
    [data, pricedModels],
  )
  const modelTokens = useMemo(
    () => (data ? pivot(data.byModel, (p) => p.input_tokens + p.output_tokens) : { groups: [], rows: [] }),
    [data],
  )

  const costLegend = useSeriesSelection(cost.groups)
  const tokensLegend = useSeriesSelection(['input', 'output'])
  const modelCostLegend = useSeriesSelection(modelCost.groups)
  const modelTokensLegend = useSeriesSelection(modelTokens.groups)

  const [costView, setCostView] = useState<ChartView>('lines')
  const [modelCostView, setModelCostView] = useState<ChartView>('lines')
  const [tokensView, setTokensView] = useState<ChartView>('lines')
  const [modelTokensView, setModelTokensView] = useState<ChartView>('lines')

  // Advisory estimates for calls the ledger recorded without a price
  // (cost NULL): the (provider, model) pairs from usageUnpriced, each
  // resolved within its own provider's catalog candidates (never the
  // whole catalog, so a model name that collides across vendors can't
  // borrow the wrong one's price). Loading/failure leaves prices at [],
  // which estimateUnpriced treats as "no estimate" — no error banner
  // needed beyond the existing widget-failure note.
  useEffect(() => {
    if (unpriced.length === 0) {
      setPrices([])
      return
    }
    let live = true
    catalogPrices(unpriced.map((g) => ({ provider: g.provider, model: g.model }))).then(
      (p) => {
        if (live) setPrices(p)
      },
      () => {
        if (live) setPrices([])
      },
    )
    return () => {
      live = false
    }
  }, [unpriced])

  // Always shown with a ≈ so estimates never masquerade as accounting.
  const estimates = useMemo(() => estimateUnpriced(unpriced, prices), [unpriced, prices])
  const estimatedTotal = totalEstimate(estimates)

  const cacheHit = useMemo(() => {
    if (!data) return 0
    const read = data.cache.reduce((n, c) => n + c.cache_read_tokens, 0)
    const fresh = data.cache.reduce((n, c) => n + c.input_tokens, 0)
    return read + fresh > 0 ? read / (read + fresh) : 0
  }, [data])

  // The dominant currency's summary row drives the header tiles — in
  // practice almost everything is one currency; any additional
  // currencies present in range get their own note below the tiles
  // rather than being folded into these numbers.
  const s = data?.summaries[0]
  const otherSummaries = data?.summaries.slice(1) ?? []
  const budget = data?.budget
  const budgetHint = (w?: { limit: { amount: number; currency: string } | null }) =>
    w?.limit != null ? `of ${money(w.limit.amount, w.limit.currency)} budget` : undefined
  const spendLabel =
    range === 'today' ? 'Spend today' : range === '7d' ? 'Spend this week' : 'Spend this month'
  const spendHint = range === 'today' ? budgetHint(budget?.day) : range === '30d' ? budgetHint(budget?.month) : undefined
  const spendOriginal = s ? secondaryMoney(s, s.cost) : undefined
  // unbilledAnnotation is the muted "+amount" note beside the spend
  // tile's value — the metered-price equivalent of subscription/
  // oauth_token executor runs (D-051), excluded from billed spend and
  // shown separately so it's visible without inflating the total.
  // Converted into the same display currency as the headline figure
  // (converted_unbilled_cost, same decorator/target as converted_amount)
  // when a stored fx rate exists, falling back to the row's own billing
  // currency otherwise (D-013: never hide the figure for a missing
  // rate). Omitted entirely when zero.
  const unbilledAnnotation =
    s && s.unbilled_cost > 0
      ? s.converted_unbilled_cost != null && s.converted_currency
        ? money(s.converted_unbilled_cost, s.converted_currency)
        : money(s.unbilled_cost, s.currency)
      : undefined
  const tiles = [
    {
      label: spendLabel,
      value: s ? primaryMoney(s, s.cost) : 'N/A',
      annotation: unbilledAnnotation,
      hint: spendOriginal ? `${spendOriginal} billed` : spendHint,
    },
    {
      label: 'Requests',
      value: s ? compact(s.requests) : 'N/A',
      hint: s ? `${s.errors} errors` : undefined,
    },
    {
      label: 'Error rate',
      value: s && s.requests > 0 ? `${((s.errors / s.requests) * 100).toFixed(1)}%` : 'N/A',
    },
    {
      label: 'Input tokens',
      value: s ? compact(s.input_tokens) : 'N/A',
      hint: s ? `${compact(s.cache_read_tokens)} cached reads` : undefined,
    },
    {
      label: 'Output tokens',
      value: s ? compact(s.output_tokens) : 'N/A',
    },
    { label: 'Cache hit', value: data ? `${(cacheHit * 100).toFixed(0)}%` : 'N/A' },
  ]

  const colorOf = (g: string, groups: string[]) => palette[groups.indexOf(g) % palette.length]

  // Budget gauges render only when a window has a configured limit —
  // day for the "today" tile's spend, month for longer ranges.
  const dayGauge = budget?.day.limit != null ? budget.day : null
  const monthGauge = budget?.month.limit != null ? budget.month : null

  return (
    <div className="h-full overflow-y-auto">
      <div className="mx-auto max-w-full px-8 py-8">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <h1 className="text-2xl font-semibold tracking-tight">Analytics</h1>
            <p className="mt-1 text-sm text-muted-foreground">
              Spend, tokens, and latency from the cost ledger.
            </p>
          </div>
          <div className="flex rounded-lg border border-border p-0.5">
            {ranges.map((r) => (
              <button
                key={r.key}
                type="button"
                onClick={() => setRange(r.key)}
                className={
                  range === r.key
                    ? 'rounded-md bg-zinc-200/80 px-3 py-1 text-sm font-medium dark:bg-zinc-800'
                    : 'rounded-md px-3 py-1 text-sm text-muted-foreground hover:text-foreground'
                }
              >
                {r.label}
              </button>
            ))}
          </div>
        </div>

        {error && (
          <div className="mt-6 rounded-xl border border-red-500/30 bg-red-500/5 p-4 text-sm text-red-600 dark:text-red-400">
            Could not load usage: {error}
          </div>
        )}

        {budget && (budget.day.over || budget.month.over) && (
          <div
            role="alert"
            className="mt-6 rounded-xl border border-amber-500/40 bg-amber-500/10 p-4 text-sm text-amber-700 dark:text-amber-400"
          >
            {[
              budget.day.over && budget.day.limit != null
                ? `Daily budget reached: ${money(budget.day.spend, budget.day.currency)} spent of ${money(budget.day.limit.amount, budget.day.limit.currency)}.`
                : null,
              budget.month.over && budget.month.limit != null
                ? `Monthly budget reached: ${money(budget.month.spend, budget.month.currency)} spent of ${money(budget.month.limit.amount, budget.month.limit.currency)}.`
                : null,
            ]
              .filter(Boolean)
              .join(' ')}
          </div>
        )}

        <div className="mt-6 grid grid-cols-2 gap-4 sm:grid-cols-3 lg:grid-cols-6">
          {tiles.map((t) => (
            <div key={t.label} className="rounded-xl border border-border p-4">
              <div className="text-xs text-muted-foreground">{t.label}</div>
              <div className="mt-1.5 text-2xl font-semibold tracking-tight">
                {t.value}
                {'annotation' in t && t.annotation && (
                  <TooltipProvider>
                    <Tooltip>
                      <TooltipTrigger asChild>
                        <span className="ml-1.5 text-xs font-normal text-muted-foreground">
                          +{t.annotation}
                        </span>
                      </TooltipTrigger>
                      <TooltipContent>unbilled</TooltipContent>
                    </Tooltip>
                  </TooltipProvider>
                )}
              </div>
              {t.hint && <div className="mt-0.5 text-xs text-muted-foreground">{t.hint}</div>}
            </div>
          ))}
        </div>

        {otherSummaries.length > 0 && (
          <p className="mt-3 text-xs text-muted-foreground">
            Also in range:{' '}
            {otherSummaries.map((o) => primaryMoney(o, o.cost)).join(', ')} (shown separately —
            never summed with the totals above).
          </p>
        )}

        {s && s.unpriced_requests > 0 && (
          <p className="mt-3 text-xs text-muted-foreground">
            {compact(s.unpriced_requests)} call{s.unpriced_requests === 1 ? '' : 's'} in range
            {' '}had no configured price and are excluded from spend
            {estimatedTotal > 0 && <>, roughly ≈{money(estimatedTotal)} at catalog prices</>}.
          </p>
        )}

        <section className="mt-6 rounded-xl border border-border p-4">
          <div className="flex items-center justify-between">
            <h2 className="text-sm font-medium">Spend over time</h2>
            <ViewToggle view={costView} onChange={setCostView} />
          </div>
          <div className="mt-3">
            <EChart
              option={(costView === 'bars' ? stackedBarsOption : multiLineOption)(
                cost.rows,
                cost.groups,
                costLegend.hidden,
                (g) => colorOf(g, cost.groups),
                (v) => bucketLabel(v, bucket),
                chartMoney,
              )}
            />
          </div>
          <StatsLegend
            rows={cost.rows}
            groups={cost.groups}
            colorOf={(g) => colorOf(g, cost.groups)}
            hidden={costLegend.hidden}
            onSelect={costLegend.onSelect}
            valueLabel={chartMoney}
          />
        </section>

        <section className="mt-6 rounded-xl border border-border p-4">
          <div className="flex items-center justify-between">
            <h2 className="text-sm font-medium">Tokens in / out</h2>
            <ViewToggle view={tokensView} onChange={setTokensView} />
          </div>
          <div className="mt-3">
            <EChart
              option={(tokensView === 'bars' ? stackedBarsOption : areaLinesOption)(
                tokens as unknown as Record<string, number | string>[],
                ['input', 'output'],
                tokensLegend.hidden,
                (g) => (g === 'input' ? palette[0] : palette[2]),
                (v) => bucketLabel(v, bucket),
                compact,
              )}
            />
          </div>
          <StatsLegend
            rows={tokens as unknown as Record<string, number | string>[]}
            groups={['input', 'output']}
            colorOf={(g) => (g === 'input' ? palette[0] : palette[2])}
            hidden={tokensLegend.hidden}
            onSelect={tokensLegend.onSelect}
            valueLabel={compact}
          />
        </section>

        <section className="mt-6 rounded-xl border border-border p-4">
          <div className="flex items-center justify-between">
            <h2 className="text-sm font-medium">Cost by model</h2>
            <ViewToggle view={modelCostView} onChange={setModelCostView} />
          </div>
          <div className="mt-3">
            <EChart
              option={(modelCostView === 'bars' ? stackedBarsOption : multiLineOption)(
                modelCost.rows,
                modelCost.groups,
                modelCostLegend.hidden,
                (g) => colorOf(g, modelCost.groups),
                (v) => bucketLabel(v, bucket),
                chartMoney,
              )}
            />
          </div>
          <StatsLegend
            rows={modelCost.rows}
            groups={modelCost.groups}
            colorOf={(g) => colorOf(g, modelCost.groups)}
            hidden={modelCostLegend.hidden}
            onSelect={modelCostLegend.onSelect}
            valueLabel={chartMoney}
          />
          {modelCost.groups.length === 0 && data && (
            <p className="mt-2 text-xs text-muted-foreground">
              No priced model usage in range (free/unpriced models are excluded from cost views).
            </p>
          )}
        </section>

        <section className="mt-6 rounded-xl border border-border p-4">
          <div className="flex items-center justify-between">
            <h2 className="text-sm font-medium">Tokens per model</h2>
            <ViewToggle view={modelTokensView} onChange={setModelTokensView} />
          </div>
          <div className="mt-3">
            <EChart
              option={(modelTokensView === 'bars' ? stackedBarsOption : multiLineOption)(
                modelTokens.rows,
                modelTokens.groups,
                modelTokensLegend.hidden,
                (g) => colorOf(g, modelTokens.groups),
                (v) => bucketLabel(v, bucket),
                compact,
              )}
            />
          </div>
          <StatsLegend
            rows={modelTokens.rows}
            groups={modelTokens.groups}
            colorOf={(g) => colorOf(g, modelTokens.groups)}
            hidden={modelTokensLegend.hidden}
            onSelect={modelTokensLegend.onSelect}
            valueLabel={compact}
          />
        </section>

        <div className="mt-6 grid gap-6 lg:grid-cols-2">
          <section className="flex flex-col rounded-xl border border-border p-4">
            <h2 className="text-sm font-medium">Latency per provider</h2>
            {data && data.latency.length > 0 ? (
              <div className="mt-3 min-h-[240px] flex-1">
                <EChart
                  fill
                  option={latencyBarsOption(
                    data.latency,
                    { p50: '#2a78d6', p95: '#eda100', p99: '#e34948' },
                    (ms) => formatDuration(Math.round(ms)),
                  )}
                />
              </div>
            ) : (
              <p className="mt-3 py-6 text-center text-sm text-muted-foreground">No requests in range.</p>
            )}
          </section>

          <section className="flex flex-col rounded-xl border border-border p-4">
            <h2 className="text-sm font-medium">Spend share</h2>
            <div className="mt-3 min-h-[240px] flex-1">
              <EChart
                fill
                option={donutOption(
                  (data?.providerTotals ?? []).filter((g) => g.cost > 0),
                  (g) => colorOf(g, cost.groups),
                  chartMoney,
                )}
              />
            </div>
            {pricedProviders.size === 0 && data && (
              <p className="mt-2 text-xs text-muted-foreground">No priced spend in range.</p>
            )}
          </section>
        </div>

        <section className="mt-6 rounded-xl border border-border p-4">
          <h2 className="text-sm font-medium">Requests &amp; errors over time</h2>
          <div className="mt-3">
            <EChart
              option={requestsErrorsOption(
                requestsErrors,
                (v) => bucketLabel(v, bucket),
                palette[0],
                palette[7],
              )}
            />
          </div>
        </section>

        {(dayGauge || monthGauge) && (
          <div className="mt-6 grid gap-6 sm:grid-cols-2">
            {dayGauge && dayGauge.limit && (
              <section className="rounded-xl border border-border p-4">
                <h2 className="text-sm font-medium">Daily budget</h2>
                <div className="mt-3">
                  <EChart
                    option={gaugeOption(
                      dayGauge.spend,
                      dayGauge.limit.amount,
                      '#1baf7a',
                      '#e34948',
                      (v) => money(v, dayGauge.limit!.currency),
                    )}
                    height={200}
                  />
                </div>
              </section>
            )}
            {monthGauge && monthGauge.limit && (
              <section className="rounded-xl border border-border p-4">
                <h2 className="text-sm font-medium">Monthly budget</h2>
                <div className="mt-3">
                  <EChart
                    option={gaugeOption(
                      monthGauge.spend,
                      monthGauge.limit.amount,
                      '#1baf7a',
                      '#e34948',
                      (v) => money(v, monthGauge.limit!.currency),
                    )}
                    height={200}
                  />
                </div>
              </section>
            )}
          </div>
        )}

        <div className="mt-6 grid gap-6 lg:grid-cols-2">
          <ProviderCostTable rows={data?.providerTotals ?? []} />
          <BreakdownTable title="By model" rows={data ? totals(data.byModel) : []} estimates={estimates} />
        </div>

        <section className="mt-6 rounded-xl border border-border p-4">
          <h2 className="text-sm font-medium">Top sessions by cost</h2>
          <table className="mt-3 w-full text-sm">
            <tbody>
              {(data?.sessions ?? []).map((sess) => (
                <tr key={sess.session_id} className="border-t border-border/60">
                  <td className="py-2 pr-2">
                    <Link
                      to={`/chat/${sess.session_id}`}
                      className="block max-w-40 truncate text-blue-600 hover:underline dark:text-blue-400"
                    >
                      {sess.session_id.slice(0, 8)}…
                    </Link>
                  </td>
                  <td className="py-2 text-right text-muted-foreground">{sess.requests} req</td>
                  <td className="py-2 text-right font-medium">
                    {primaryMoney(sess, sess.cost)}
                    {secondaryMoney(sess, sess.cost) && (
                      <span className="ml-1 text-xs font-normal text-muted-foreground">
                        ({secondaryMoney(sess, sess.cost)})
                      </span>
                    )}
                  </td>
                </tr>
              ))}
              {data && data.sessions.length === 0 && (
                <tr>
                  <td className="py-6 text-center text-muted-foreground">
                    No session spend in range.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </section>
      </div>
    </div>
  )
}

function BreakdownTable({
  title,
  rows,
  estimates,
}: {
  title: string
  rows: TotalsRow[]
  estimates?: Map<string, number>
}) {
  return (
    <section className="rounded-xl border border-border p-4">
      <h2 className="text-sm font-medium">{title}</h2>
      <table className="mt-3 w-full text-sm">
        <tbody>
          {rows.map((t) => (
            <tr key={`${t.group} ${t.currency}`} className="border-t border-border/60">
              <td className="max-w-36 truncate py-2 pr-2">{t.group}</td>
              <td className="py-2 text-right text-muted-foreground">{compact(t.tokens)} tok</td>
              <td className="py-2 text-right font-medium">
                {primaryMoney(t, t.cost)}
                {secondaryMoney(t, t.cost) && (
                  <span className="ml-1 text-xs font-normal text-muted-foreground">
                    ({secondaryMoney(t, t.cost)})
                  </span>
                )}
                {(estimates?.get(t.group) ?? 0) > 0 && (
                  <span
                    className="ml-1 text-xs font-normal text-muted-foreground"
                    title="Estimated from catalog prices for calls with no configured price."
                  >
                    +≈{money(estimates?.get(t.group) ?? 0, t.currency)}
                  </span>
                )}
              </td>
            </tr>
          ))}
          {rows.length === 0 && (
            <tr>
              <td className="py-6 text-center text-muted-foreground">Nothing in range.</td>
            </tr>
          )}
        </tbody>
      </table>
    </section>
  )
}

type SortKey = 'group' | 'cost' | 'requests'

// ProviderCostTable is the sortable "cost by provider" table backed by
// usageTotals — the non-time-bucketed sibling of the stacked bar chart
// above it. Zero-cost providers (unpriced NULL rows tallied elsewhere,
// or a genuinely free local model) are excluded: they add no signal to
// a cost ranking. Restricted to the dominant (first-seen) currency so
// "% of total" stays a meaningful ratio — summing % across currencies
// would be as wrong as summing the cost itself.
function ProviderCostTable({ rows }: { rows: GroupTotal[] }) {
  const [sortKey, setSortKey] = useState<SortKey>('cost')
  const [asc, setAsc] = useState(false)

  const currency = rows[0]?.currency ?? 'USD'
  const priced = useMemo(
    () => rows.filter((r) => r.cost > 0 && r.currency === currency),
    [rows, currency],
  )
  const totalCost = useMemo(() => priced.reduce((n, r) => n + r.cost, 0), [priced])
  const sorted = useMemo(() => {
    const copy = [...priced]
    copy.sort((a, b) => {
      const dir = asc ? 1 : -1
      if (sortKey === 'group') return a.group.localeCompare(b.group) * dir
      return (a[sortKey] - b[sortKey]) * dir
    })
    return copy
  }, [priced, sortKey, asc])

  const toggleSort = (key: SortKey) => {
    if (key === sortKey) setAsc((a) => !a)
    else {
      setSortKey(key)
      setAsc(false)
    }
  }

  const sortIndicator = (key: SortKey) => (sortKey === key ? (asc ? ' ▲' : ' ▼') : '')

  return (
    <section className="rounded-xl border border-border p-4">
      <h2 className="text-sm font-medium">Provider cost breakdown</h2>
      <table className="mt-3 w-full text-sm">
        <thead>
          <tr className="text-left text-xs text-muted-foreground">
            <th className="cursor-pointer pb-2 font-medium" onClick={() => toggleSort('group')}>
              Provider{sortIndicator('group')}
            </th>
            <th
              className="cursor-pointer pb-2 text-right font-medium"
              onClick={() => toggleSort('requests')}
            >
              Requests{sortIndicator('requests')}
            </th>
            <th
              className="cursor-pointer pb-2 text-right font-medium"
              onClick={() => toggleSort('cost')}
            >
              Cost{sortIndicator('cost')}
            </th>
            <th className="pb-2 text-right font-medium">% of total</th>
          </tr>
        </thead>
        <tbody>
          {sorted.map((r) => (
            <tr key={r.group} className="border-t border-border/60">
              <td className="max-w-28 truncate py-2 pr-2">{r.group}</td>
              <td className="py-2 text-right text-muted-foreground">{compact(r.requests)}</td>
              <td className="py-2 text-right font-medium">
                {primaryMoney(r, r.cost)}
                {secondaryMoney(r, r.cost) && (
                  <span className="ml-1 text-xs font-normal text-muted-foreground">
                    ({secondaryMoney(r, r.cost)})
                  </span>
                )}
              </td>
              <td className="py-2 text-right text-muted-foreground">
                {totalCost > 0 ? `${((r.cost / totalCost) * 100).toFixed(0)}%` : 'N/A'}
              </td>
            </tr>
          ))}
          {sorted.length === 0 && (
            <tr>
              <td colSpan={4} className="py-6 text-center text-muted-foreground">
                Nothing in range.
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </section>
  )
}
