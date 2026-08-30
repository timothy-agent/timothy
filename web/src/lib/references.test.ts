import { describe, expect, it } from 'vitest'
import type { Reference } from '../api/types'
import { addReference, groupReferenceOptions, maxReferences, removeReference } from './references'

describe('addReference', () => {
  it('appends a new reference', () => {
    const refs: Reference[] = []
    const next = addReference(refs, { kind: 'mission', id: 'm1', name: 'Mission one' })
    expect(next).toEqual([{ kind: 'mission', id: 'm1', name: 'Mission one' }])
  })

  it('dedupes by kind+id', () => {
    const refs: Reference[] = [{ kind: 'mission', id: 'm1', name: 'Mission one' }]
    const next = addReference(refs, { kind: 'mission', id: 'm1', name: 'Mission one' })
    expect(next).toBe(refs)
  })

  it('refuses to add past maxReferences', () => {
    const refs: Reference[] = Array.from({ length: maxReferences }, (_, i) => ({
      kind: 'mission' as const,
      id: `m${i}`,
      name: `Mission ${i}`,
    }))
    const next = addReference(refs, { kind: 'session', id: 's1', name: 'A chat' })
    expect(next).toBe(refs)
    expect(next).toHaveLength(maxReferences)
  })
})

describe('removeReference', () => {
  it('removes only the matching kind+id', () => {
    const refs: Reference[] = [
      { kind: 'mission', id: 'm1', name: 'Mission one' },
      { kind: 'session', id: 'm1', name: 'Same id, different kind' },
    ]
    const next = removeReference(refs, 'mission', 'm1')
    expect(next).toEqual([{ kind: 'session', id: 'm1', name: 'Same id, different kind' }])
  })
})

describe('groupReferenceOptions', () => {
  it('groups by kind in a fixed order, omitting empty groups', () => {
    const groups = groupReferenceOptions([
      { kind: 'kb_doc', id: 'd1', name: 'Doc' },
      { kind: 'mission', id: 'm1', name: 'Mission' },
    ])
    expect(groups.map((g) => g.kind)).toEqual(['mission', 'kb_doc'])
  })
})
