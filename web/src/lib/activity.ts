import type { ToolRun } from './chat'

// prettyArgs pretty-prints a tool call's JSON args; malformed or
// non-JSON input renders raw rather than throwing.
export function prettyArgs(raw: string): string {
  try {
    return JSON.stringify(JSON.parse(raw), null, 2)
  } catch {
    return raw
  }
}

// totalDuration sums the wall time of every tool call that reported
// one. Calls still running (no durationMs yet) contribute nothing —
// an honest "0" rather than a guess is the caller's job to phrase.
export function totalDuration(tools: ToolRun[]): number {
  return tools.reduce((sum, t) => sum + (t.durationMs ?? 0), 0)
}

// summarizeTools names what a turn did, deduped by first appearance
// and capped so a turn with a dozen tool calls still reads as one
// line: "search_web, 2× fetch_url, +2 more".
export function summarizeTools(tools: ToolRun[]): string {
  const order: string[] = []
  const counts = new Map<string, number>()
  for (const t of tools) {
    if (!counts.has(t.name)) order.push(t.name)
    counts.set(t.name, (counts.get(t.name) ?? 0) + 1)
  }
  const names = order.map((name) => {
    const n = counts.get(name) ?? 1
    return n > 1 ? `${n}× ${name}` : name
  })
  if (names.length <= 3) return names.join(', ')
  return `${names.slice(0, 3).join(', ')}, +${names.length - 3} more`
}
