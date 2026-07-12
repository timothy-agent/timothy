// splitSources pulls a trailing "## Sources" markdown section off an
// answer so the UI can render it as a distinct panel instead of just
// another heading in the prose. Only the LAST such heading counts —
// an answer that discusses "sources" mid-body must not be truncated.
export interface Citation {
  title: string
  url: string
}

export interface SplitAnswer {
  body: string
  citations: Citation[]
}

const sourcesHeading = /\n##\s*Sources\s*\n/gi
const citationLine = /^\s*\d+\.\s*\[([^\]]+)\]\(([^)]+)\)\s*$/

export function splitSources(text: string): SplitAnswer {
  let match: RegExpExecArray | null = null
  for (let m = sourcesHeading.exec(text); m; m = sourcesHeading.exec(text)) match = m
  if (!match) return { body: text, citations: [] }

  const body = text.slice(0, match.index).trimEnd()
  const tail = text.slice(match.index + match[0].length)

  const citations: Citation[] = []
  for (const line of tail.split('\n')) {
    const m = citationLine.exec(line)
    if (m) citations.push({ title: m[1], url: m[2] })
  }
  // A heading with no parseable lines under it is not a real citation
  // list (model may have written prose under a coincidental heading);
  // keep the whole thing in the body rather than silently eating it.
  if (citations.length === 0) return { body: text, citations: [] }

  return { body, citations }
}
