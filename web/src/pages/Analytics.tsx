import { useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router'
import {
  Bar,
  BarChart,
  CartesianGrid,
  Legend,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts'
import {
  usageBudget,
  usageCache,
  usageLatency,
  usageSeries,
  usageSessions,
  usageSummary,
} from '../api/client'
import type {
  BudgetStatus,
  CacheRow,
  LatencyRow,
  SessionUsage,
  UsagePoint,
  UsageSummary,
} from '../api/types'

// Accessible categorical palette that reads on light and dark.
const palette = ['#3b82f6', '#8b5cf6', '#10b981', '#f59e0b', '#f43f5e', '#06b6d4', '#a3a3a3']

const ranges = [
  { key: 'today', label: 'Today', days: 0, bucket: 'hour' as const },
  { key: '7d', label: '7 days', days: 7, bucket: 'day' as const },
  { key: '30d', label: '30 days', days: 30, bucket: 'day' as const },
]

function rangeDates(key: string): { from: Date; to: Date; bucket: 'hour' | 'day' } {
  const r = ranges.find((x) => x.key === key) ?? ranges[2]
  const to = new Date()
  const from = new Date()
  if (r.days === 0) from.setHours(0, 0, 0, 0)
  else from.setDate(from.getDate() - r.days)
  return { from, to, bucket: r.bucket }
}

function money(v: number): string {
  return v >= 1 ? `$${v.toFixed(2)}` : `$${v.toFixed(4)}`
}

function compact(v: number): string {
  if (v >= 1_000_000) return `${(v / 1_000_000).toFixed(1)}M`
  if (v >= 1_000) return `${(v / 1_000).toFixed(1)}k`
  return String(v)
}

interface Loaded {
  summary: UsageSummary
  month: UsageSummary
  today: UsageSummary
  byProvider: UsagePoint[]
  byModel: UsagePoint[]
  byRoute: UsagePoint[]
  sessions: SessionUsage[]
  latency: LatencyRow[]
  cache: CacheRow[]
  budget: BudgetStatus | null
}

// pivot turns (bucket, group) points into recharts rows keyed by
// bucket with one column per group.
function pivot(points: UsagePoint[], metric: (p: UsagePoint) => number) {
  const groups = [...new Set(points.map((p) => p.group))]
  const rows = new Map<string, Record<string, number | string>>()
  for (const p of points) {
    const key = p.bucket
    const row = rows.get(key) ?? { bucket: key }
    row[p.group] = ((row[p.group] as number) ?? 0) + metric(p)
    rows.set(key, row)
  }
  return { groups, rows: [...rows.values()] }
}

// totals collapses a series into one row per group for the tables.
function totals(points: UsagePoint[]) {
  const acc = new Map<string, { cost: number; requests: number; errors: number; tokens: number }>()
  for (const p of points) {
    const t = acc.get(p.group) ?? { cost: 0, requests: 0, errors: 0, tokens: 0 }
    t.cost += p.cost_usd
    t.requests += p.requests
    t.errors += p.errors
    t.tokens += p.input_tokens + p.output_tokens
    acc.set(p.group, t)
  }
  return [...acc.entries()].sort((a, b) => b[1].cost - a[1].cost)
}

const bucketLabel = (iso: string, bucket: string) => {
  const d = new Date(iso)
  return bucket === 'hour'
    ? d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
    : d.toLocaleDateString([], { month: 'short', day: 'numeric' })
}

export function Analytics() {
  const [range, setRange] = useState('7d')
  const [data, setData] = useState<Loaded | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    const { from, to, bucket } = rangeDates(range)
    const startOfToday = new Date()
    startOfToday.setHours(0, 0, 0, 0)
    const startOfMonth = new Date()
    startOfMonth.setDate(1)
    startOfMonth.setHours(0, 0, 0, 0)

    const emptySummary: UsageSummary = {
      cost_usd: 0,
      input_tokens: 0,
      output_tokens: 0,
      cache_read_tokens: 0,
      cache_write_tokens: 0,
      requests: 0,
      errors: 0,
    }

    let live = true
    setError(null)
    Promise.allSettled([
      usageSummary(from, to),
      usageSummary(startOfMonth, to),
      usageSummary(startOfToday, to),
      usageSeries(from, to, bucket, 'provider'),
      usageSeries(from, to, bucket, 'model'),
      usageSeries(from, to, bucket, 'route'),
      usageSessions(from, to, 10),
      usageLatency(from, to),
      usageCache(from, to),
      usageBudget(),
    ]).then((results) => {
      if (!live) return
      const failed = results.filter((r) => r.status === 'rejected').length
      setError(failed > 0 ? `${failed} of ${results.length} widgets failed to load` : null)
      function val<T>(r: PromiseSettledResult<T>, fallback: T): T {
        return r.status === 'fulfilled' ? r.value : fallback
      }
      setData({
        summary: val(results[0], emptySummary),
        month: val(results[1], emptySummary),
        today: val(results[2], emptySummary),
        byProvider: val(results[3], []),
        byModel: val(results[4], []),
        byRoute: val(results[5], []),
        sessions: val(results[6], []),
        latency: val(results[7], []),
        cache: val(results[8], []),
        budget: val<BudgetStatus | null>(results[9], null),
      })
    })
    return () => {
      live = false
    }
  }, [range])

  const { bucket } = rangeDates(range)
  const cost = useMemo(
    () => (data ? pivot(data.byProvider, (p) => p.cost_usd) : { groups: [], rows: [] }),
    [data],
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

  const cacheHit = useMemo(() => {
    if (!data) return 0
    const read = data.cache.reduce((n, c) => n + c.cache_read_tokens, 0)
    const fresh = data.cache.reduce((n, c) => n + c.input_tokens, 0)
    return read + fresh > 0 ? read / (read + fresh) : 0
  }, [data])

  const s = data?.summary
  const budget = data?.budget
  const budgetHint = (w?: { limit_usd: number | null }) =>
    w?.limit_usd != null ? `of ${money(w.limit_usd)} budget` : undefined
  const tiles = [
    { label: 'Spend today', value: data ? money(data.today.cost_usd) : '—', hint: budgetHint(budget?.day) },
    {
      label: 'Spend this month',
      value: data ? money(data.month.cost_usd) : '—',
      hint: budgetHint(budget?.month),
    },
    {
      label: 'Requests',
      value: s ? compact(s.requests) : '—',
      hint: s ? `${s.errors} errors` : undefined,
    },
    {
      label: 'Error rate',
      value: s && s.requests > 0 ? `${((s.errors / s.requests) * 100).toFixed(1)}%` : '—',
    },
    {
      label: 'Input tokens',
      value: s ? compact(s.input_tokens) : '—',
      hint: s ? `${compact(s.cache_read_tokens)} cached reads` : undefined,
    },
    {
      label: 'Output tokens',
      value: s ? compact(s.output_tokens) : '—',
    },
    { label: 'Cache hit', value: data ? `${(cacheHit * 100).toFixed(0)}%` : '—' },
  ]

  const axis = { stroke: 'currentColor', opacity: 0.45, fontSize: 11 }
  const tooltipStyle = {
    backgroundColor: 'var(--color-zinc-900, #18181b)',
    border: '1px solid rgba(255,255,255,0.1)',
    borderRadius: 8,
    color: '#fafafa',
    fontSize: 12,
  }

  return (
    <div className="h-full overflow-y-auto">
      <div className="mx-auto max-w-6xl py-8">
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
              budget.day.over && budget.day.limit_usd != null
                ? `Daily budget reached: ${money(budget.day.spend_usd)} spent of ${money(budget.day.limit_usd)}.`
                : null,
              budget.month.over && budget.month.limit_usd != null
                ? `Monthly budget reached: ${money(budget.month.spend_usd)} spent of ${money(budget.month.limit_usd)}.`
                : null,
            ]
              .filter(Boolean)
              .join(' ')}
          </div>
        )}

        <div className="mt-6 grid grid-cols-2 gap-4 sm:grid-cols-4 lg:grid-cols-7">
          {tiles.map((t) => (
            <div key={t.label} className="rounded-xl border border-border p-4">
              <div className="text-xs text-muted-foreground">{t.label}</div>
              <div className="mt-1.5 text-2xl font-semibold tracking-tight">{t.value}</div>
              {t.hint && <div className="mt-0.5 text-xs text-muted-foreground">{t.hint}</div>}
            </div>
          ))}
        </div>

        <div className="mt-6 grid gap-6 lg:grid-cols-2">
          <section className="rounded-xl border border-border p-4">
            <h2 className="text-sm font-medium">Cost by provider</h2>
            <div className="mt-3 h-64">
              <ResponsiveContainer>
                <BarChart data={cost.rows}>
                  <CartesianGrid strokeOpacity={0.12} vertical={false} />
                  <XAxis
                    dataKey="bucket"
                    tickFormatter={(v: string) => bucketLabel(v, bucket)}
                    {...axis}
                  />
                  <YAxis tickFormatter={(v: number) => money(v)} width={70} {...axis} />
                  <Tooltip
                    contentStyle={tooltipStyle}
                    labelFormatter={(v) => bucketLabel(String(v), bucket)}
                    formatter={(v) => money(Number(v))}
                  />
                  <Legend wrapperStyle={{ fontSize: 12 }} />
                  {cost.groups.map((g, i) => (
                    <Bar key={g} dataKey={g} stackId="cost" fill={palette[i % palette.length]} />
                  ))}
                </BarChart>
              </ResponsiveContainer>
            </div>
          </section>

          <section className="rounded-xl border border-border p-4">
            <h2 className="text-sm font-medium">Tokens in / out</h2>
            <div className="mt-3 h-64">
              <ResponsiveContainer>
                <LineChart data={tokens}>
                  <CartesianGrid strokeOpacity={0.12} vertical={false} />
                  <XAxis
                    dataKey="bucket"
                    tickFormatter={(v: string) => bucketLabel(v, bucket)}
                    {...axis}
                  />
                  <YAxis tickFormatter={(v: number) => compact(v)} width={55} {...axis} />
                  <Tooltip
                    contentStyle={tooltipStyle}
                    labelFormatter={(v) => bucketLabel(String(v), bucket)}
                    formatter={(v) => compact(Number(v))}
                  />
                  <Legend wrapperStyle={{ fontSize: 12 }} />
                  <Line type="monotone" dataKey="input" stroke={palette[0]} dot={false} strokeWidth={2} />
                  <Line type="monotone" dataKey="output" stroke={palette[2]} dot={false} strokeWidth={2} />
                </LineChart>
              </ResponsiveContainer>
            </div>
          </section>
        </div>

        <div className="mt-6 grid gap-6 lg:grid-cols-3">
          <BreakdownTable title="By model" rows={data ? totals(data.byModel) : []} />
          <BreakdownTable title="By route" rows={data ? totals(data.byRoute) : []} />
          <section className="rounded-xl border border-border p-4">
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
                    <td className="py-2 text-right font-medium">{money(sess.cost_usd)}</td>
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

        <section className="mt-6 rounded-xl border border-border p-4">
          <h2 className="text-sm font-medium">Latency per provider</h2>
          <div className="overflow-x-auto">
            <table className="mt-3 w-full min-w-md text-sm">
              <thead>
                <tr className="text-left text-xs text-muted-foreground">
                  <th className="pb-2 font-medium">Provider</th>
                  <th className="pb-2 text-right font-medium">p50</th>
                  <th className="pb-2 text-right font-medium">p95</th>
                  <th className="pb-2 text-right font-medium">p99</th>
                  <th className="pb-2 text-right font-medium">Requests</th>
                </tr>
              </thead>
              <tbody>
                {(data?.latency ?? []).map((l) => (
                  <tr key={l.provider} className="border-t border-border/60">
                    <td className="py-2">{l.provider}</td>
                    <td className="py-2 text-right">{Math.round(l.p50_ms)} ms</td>
                    <td className="py-2 text-right">{Math.round(l.p95_ms)} ms</td>
                    <td className="py-2 text-right">{Math.round(l.p99_ms)} ms</td>
                    <td className="py-2 text-right text-muted-foreground">{compact(l.requests)}</td>
                  </tr>
                ))}
                {data && data.latency.length === 0 && (
                  <tr>
                    <td colSpan={5} className="py-6 text-center text-muted-foreground">
                      No requests in range.
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        </section>
      </div>
    </div>
  )
}

function BreakdownTable({
  title,
  rows,
}: {
  title: string
  rows: [string, { cost: number; requests: number; errors: number; tokens: number }][]
}) {
  return (
    <section className="rounded-xl border border-border p-4">
      <h2 className="text-sm font-medium">{title}</h2>
      <table className="mt-3 w-full text-sm">
        <tbody>
          {rows.map(([group, t]) => (
            <tr key={group} className="border-t border-border/60">
              <td className="max-w-36 truncate py-2 pr-2">{group}</td>
              <td className="py-2 text-right text-muted-foreground">{compact(t.tokens)} tok</td>
              <td className="py-2 text-right font-medium">{money(t.cost)}</td>
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
