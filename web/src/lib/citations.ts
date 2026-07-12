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

// collapseRepeatedTail strips a verbatim duplicated tail: models
// occasionally restate their entire answer (Sources section included)
// after a late tool call, and the stream concatenates both copies.
// Mirrors the server-side collapse so the live view matches what the
// brain persists. The 40-char floor keeps legitimately repeated short
// phrases intact.
export function collapseRepeatedTail(s: string): string {
  const minRepeat = 40
  const t = s.replace(/[ \t\n]+$/, '')
  const n = t.length
  for (let l = Math.floor(n / 2); l >= minRepeat; l--) {
    const tail = t.slice(n - l)
    const head = t.slice(0, n - l).replace(/[ \t\n]+$/, '')
    if (head.endsWith(tail)) return collapseRepeatedTail(head)
  }
  return n === s.length ? s : t
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
