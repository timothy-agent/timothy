import { useEffect, useRef, useState } from 'react'
import { listMissions, listSessions, searchKbDocuments } from '../api/client'
import type { Reference, ReferenceKind } from '../api/types'

// maxReferences mirrors the server's cap (internal/brain/chat/
// references.go's maxReferences) so the UI disables adding beyond it
// rather than letting a request get rejected.
export const maxReferences = 8

// referenceSearchDebounceMs matches the model catalog search's own
// debounce (ModelInput.tsx's useCatalogSearch) — one request per pause
// in typing, not per keystroke.
const referenceSearchDebounceMs = 250

// ReferenceOption is one row of the # picker's results, grouped by
// kind for the popup's section headers.
export interface ReferenceOption {
  kind: ReferenceKind
  id: string
  name: string
}

// searchReferences runs the three lookups in parallel and returns
// whatever each yields as its own group — a failed lookup degrades to
// an empty group rather than failing the whole search. Missions and
// sessions are already newest-first from their list endpoints, so an
// empty query still returns a usable "recent" list; kb documents
// require at least one character (searchKbDocuments with no query
// would return every document across every collection).
async function searchReferences(q: string): Promise<ReferenceOption[]> {
  const trimmed = q.trim()
  const [missions, sessions, docs] = await Promise.all([
    listMissions({ query: trimmed, limit: 8 }).catch(() => []),
    listSessions(trimmed).catch(() => []),
    trimmed ? searchKbDocuments(trimmed).catch(() => []) : Promise.resolve([]),
  ])
  return [
    ...missions.map((m) => ({ kind: 'mission' as const, id: m.id, name: m.name || m.goal })),
    ...sessions.map((s) => ({ kind: 'session' as const, id: s.id, name: s.title || s.id })),
    ...docs.map((d) => ({ kind: 'kb_doc' as const, id: d.id, name: d.title })),
  ]
}

// useReferenceSearch debounces q and fires searchReferences, guarding
// against out-of-order responses with a request-sequence counter (same
// pattern as useCatalogSearch) — active only while q is non-null (the
// picker is open).
export function useReferenceSearch(q: string | null): ReferenceOption[] {
  const [options, setOptions] = useState<ReferenceOption[]>([])
  const seq = useRef(0)

  useEffect(() => {
    if (q === null) {
      setOptions([])
      return
    }
    const mySeq = ++seq.current
    const t = setTimeout(() => {
      searchReferences(q).then(
        (result) => {
          if (mySeq === seq.current) setOptions(result)
        },
        () => {
          if (mySeq === seq.current) setOptions([])
        },
      )
    }, referenceSearchDebounceMs)
    return () => clearTimeout(t)
  }, [q])

  return options
}

// referenceKindLabel names each group's section header in the popup.
export const referenceKindLabel: Record<ReferenceKind, string> = {
  mission: 'Missions',
  session: 'Chats',
  kb_doc: 'Documents',
}

export function groupReferenceOptions(
  options: ReferenceOption[],
): { kind: ReferenceKind; options: ReferenceOption[] }[] {
  return (['mission', 'session', 'kb_doc'] as ReferenceKind[])
    .map((kind) => ({ kind, options: options.filter((o) => o.kind === kind) }))
    .filter((g) => g.options.length > 0)
}

// addReference appends a picked option as a reference chip, deduped by
// kind+id and capped at maxReferences — a no-op past the cap (the
// caller disables the option in the UI, this is the belt-and-suspenders
// guard for a click that raced past it).
export function addReference(refs: Reference[], picked: ReferenceOption): Reference[] {
  if (refs.some((r) => r.kind === picked.kind && r.id === picked.id)) return refs
  if (refs.length >= maxReferences) return refs
  return [...refs, picked]
}

export function removeReference(refs: Reference[], kind: ReferenceKind, id: string): Reference[] {
  return refs.filter((r) => !(r.kind === kind && r.id === id))
}
