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

// money labels an amount with its billing currency code rather than
// assuming "$"/USD — the ledger itself never converts (D-013): this
// always renders the amount exactly as recorded.
export function money(v: number, currency = 'USD'): string {
  if (v === 0) return `${currency} 0`
  return v >= 1 ? `${currency} ${v.toFixed(2)}` : `${currency} ${v.toFixed(4)}`
}
