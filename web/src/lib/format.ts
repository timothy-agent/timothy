// Compact number formatting for dashboard-style stats (token counts, call counts).
export function compact(v: number): string {
  if (v >= 1_000_000) return `${(v / 1_000_000).toFixed(1)}M`
  if (v >= 1_000) return `${(v / 1_000).toFixed(1)}k`
  return String(v)
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
