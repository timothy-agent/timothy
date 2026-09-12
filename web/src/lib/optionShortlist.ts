// Detects and parses a ranked-option shortlist artifact: level-3
// sections carrying bold-labeled fields (the shape the hackathon
// skill's ideas.md uses), optionally followed by a `## Dropped`
// section listing cut candidates one line each.

export type OptionField = { label: string; text: string }

export type ShortlistOption = {
  name: string
  pitch: string
  fields: OptionField[]
  rawMarkdown: string
}

export type DroppedEntry = { name?: string; reason: string }

export type OptionShortlist = {
  options: ShortlistOption[]
  dropped?: { entries: DroppedEntry[] }
}

// Labels an option section must carry to count as the option template.
const REQUIRED_LABELS = [
  'pitch',
  'fit',
  'differentiation',
  'build',
  'demo',
  'why choose it',
  'why not',
]

const HEADING_RE = /^(#{2,3})\s+(.+?)\s*#*\s*$/
const FIELD_RE = /^\*\*(.+?):?\*\*:?\s*(.*)$/

function normalizeLabel(label: string): string {
  return label.trim().replace(/\s+/g, ' ').toLowerCase()
}

function parseFields(body: string[]): OptionField[] {
  const fields: OptionField[] = []
  for (const line of body) {
    const m = FIELD_RE.exec(line.trim())
    if (m) {
      fields.push({ label: m[1].trim(), text: m[2].trim() })
      continue
    }
    if (fields.length > 0 && line.trim() !== '') {
      const last = fields[fields.length - 1]
      last.text = last.text === '' ? line.trim() : `${last.text}\n${line.trim()}`
    }
  }
  return fields
}

// Dropped entries are one line each ("`## Dropped` also carries the
// pool candidates that were cut, one line each with the reason"), so
// they parse as list items, with an optional bold name prefix.
function parseDropped(body: string[]): DroppedEntry[] {
  const entries: DroppedEntry[] = []
  for (const raw of body) {
    const line = raw.trim()
    if (line === '') continue
    const item = /^(?:[-*+]|\d+\.)\s+(.*)$/.exec(line)
    const text = item ? item[1].trim() : line
    if (text === '') continue
    const named = /^\*\*(.+?):?\*\*:?\s*(.*)$/.exec(text)
    if (named && named[2].trim() !== '') {
      entries.push({ name: named[1].trim(), reason: named[2].trim() })
      continue
    }
    const colon = /^([^:]{1,80}):\s*(.+)$/.exec(text)
    if (colon) {
      entries.push({ name: colon[1].trim(), reason: colon[2].trim() })
      continue
    }
    entries.push({ reason: text })
  }
  return entries
}

// parseOptionShortlist returns the parsed shortlist, or null when the
// document does not follow the option template.
export function parseOptionShortlist(text: string): OptionShortlist | null {
  if (!text.trim()) return null
  const lines = text.split('\n')

  type Block = { level: number; title: string; body: string[]; raw: string[] }
  const blocks: Block[] = []
  let current: Block | undefined
  let inFence = false
  for (const line of lines) {
    if (/^\s*(```|~~~)/.test(line)) inFence = !inFence
    const m = inFence ? null : HEADING_RE.exec(line)
    if (m) {
      current = { level: m[1].length, title: m[2].trim(), body: [], raw: [line] }
      blocks.push(current)
      continue
    }
    if (current) {
      current.body.push(line)
      current.raw.push(line)
    }
  }

  const options: ShortlistOption[] = []
  let dropped: { entries: DroppedEntry[] } | undefined
  for (const block of blocks) {
    if (block.level === 2) {
      if (normalizeLabel(block.title) === 'dropped') {
        const entries = parseDropped(block.body)
        if (entries.length > 0) dropped = { entries }
      }
      continue
    }
    const fields = parseFields(block.body)
    const labels = new Set(fields.map((f) => normalizeLabel(f.label)))
    if (!REQUIRED_LABELS.every((l) => labels.has(l))) continue
    const pitch = fields.find((f) => normalizeLabel(f.label) === 'pitch')?.text ?? ''
    options.push({
      name: block.title,
      pitch,
      fields,
      rawMarkdown: block.raw.join('\n').trimEnd(),
    })
  }

  if (options.length === 0) return null
  return dropped ? { options, dropped } : { options }
}

// followUpGoal formats a picked option as follow-up goal text, in the
// objective/scope shape the followup_mission brief uses.
export function followUpGoal(option: ShortlistOption): string {
  const build = option.fields.find((f) => normalizeLabel(f.label) === 'build')?.text ?? ''
  const parts = [`Build "${option.name}".`, '', `Objective: ${option.pitch}`]
  if (build.trim() !== '') parts.push(`Scope: ${build}`)
  return parts.join('\n')
}
