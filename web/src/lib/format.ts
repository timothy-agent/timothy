// Compact number formatting for dashboard-style stats (token counts, call counts).
// Sub-1k values are whole counts most of the time (a token/request tally),
// but a derived stat like a legend's mean can land on a fraction — rounded
// to 2 decimals so it never renders with raw float precision.
export function compact(v: number): string {
  if (v >= 1_000_000) return `${(v / 1_000_000).toFixed(1)}M`
  if (v >= 1_000) return `${(v / 1_000).toFixed(1)}k`
  return String(Math.round(v * 100) / 100)
}

// formatDuration renders a wall time compactly: sub-second as
// milliseconds, sub-minute as one decimal of seconds, otherwise whole
// minutes plus whole seconds (e.g. "1m 21s") — a turn's duration can
// run minutes, unlike a single tool call.
export function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`
  const totalSeconds = Math.round(ms / 1000)
  const minutes = Math.floor(totalSeconds / 60)
  const seconds = totalSeconds % 60
  return `${minutes}m ${seconds}s`
}

// missionDisplayName renders a mission's short name when generation
// landed, falling back to the goal truncated to n runes (default 60)
// — the same "generation hasn't happened / never will" fallback chat
// sessions use ('New session'), but goal-derived since a mission's
// goal is always meaningful text, unlike a fresh empty chat.
export function missionDisplayName(mission: { name?: string; goal: string }, n = 60): string {
  if (mission.name) return mission.name
  const goal = mission.goal
  return goal.length > n ? `${goal.slice(0, n)}…` : goal
}

// humanBytes renders a byte count in the largest unit that keeps it
// readable (KB/MB/GB), one decimal past the first unit.
export function humanBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let v = bytes / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(1)} ${units[i]}`
}

// relativeTime renders an ISO timestamp as "just now" / "5m ago" /
// "3h ago" / "2d ago", falling back to a locale date past a week —
// document/collection rows update often enough that an absolute
// timestamp is less scannable than a relative one.
export function relativeTime(iso: string): string {
  const ms = Date.now() - new Date(iso).getTime()
  if (ms < 60_000) return 'just now'
  const minutes = Math.floor(ms / 60_000)
  if (minutes < 60) return `${minutes}m ago`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h ago`
  const days = Math.floor(hours / 24)
  if (days < 7) return `${days}d ago`
  return new Date(iso).toLocaleDateString()
}

// relativeTimeUntil renders a future ISO timestamp as "in 5m" / "in
// 3h" / "in 2d" — relativeTime's ms is negative for a future
// timestamp, so it always falls into its "just now" branch regardless
// of how far out the time actually is.
export function relativeTimeUntil(iso: string): string {
  const ms = new Date(iso).getTime() - Date.now()
  if (ms < 60_000) return 'due now'
  const minutes = Math.floor(ms / 60_000)
  if (minutes < 60) return `in ${minutes}m`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `in ${hours}h`
  const days = Math.floor(hours / 24)
  if (days < 7) return `in ${days}d`
  return new Date(iso).toLocaleDateString()
}

// money renders an amount in its billing currency's own symbol rather
// than assuming "$"/USD — the ledger itself never converts (D-013):
// this always renders the amount exactly as recorded. Precision keeps
// the pre-Intl convention: exact zero gets no decimals, sub-1 amounts
// get 4 (fractions-of-a-cent costs are common here), everything else
// gets 2. An unrecognized currency code (typo, future code Intl
// doesn't know yet) must never throw — falls back to "CODE amount".
export function money(v: number, currency = 'USD'): string {
  const digits = v === 0 ? 0 : Math.abs(v) >= 1 ? 2 : 4
  try {
    return new Intl.NumberFormat(undefined, {
      style: 'currency',
      currency,
      currencyDisplay: 'narrowSymbol',
      minimumFractionDigits: digits,
      maximumFractionDigits: digits,
    }).format(v)
  } catch {
    return `${currency} ${v.toFixed(digits)}`
  }
}
