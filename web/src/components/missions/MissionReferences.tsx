import { Cancel01Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import type { ComponentProps, KeyboardEvent } from 'react'
import { useRef, useState } from 'react'
import type { Reference } from '../../api/types'
import {
  addReference,
  groupReferenceOptions,
  maxReferences,
  referenceKindLabel,
  removeReference,
  useReferenceSearch,
} from '../../lib/references'
import { Textarea } from '../ui/textarea'

const mentionRe = /(^|\s)#([a-zA-Z0-9_-]*)$/

// GoalTextarea is the mission-create form's goal input with a `#`
// reference picker attached: typing `#` opens an autocomplete over
// missions/chats/kb documents (same mechanism as Composer.tsx's own
// #-mention, built fresh here since the goal field never had one) —
// picking an entry inserts a removable chip and strips the `#query`
// token from the goal text.
export function GoalTextarea({
  value,
  onChange,
  references,
  onReferences,
  ...textareaProps
}: {
  value: string
  onChange: (v: string) => void
  references: Reference[]
  onReferences: (next: Reference[]) => void
} & Omit<ComponentProps<typeof Textarea>, 'value' | 'onChange'>) {
  const inputRef = useRef<HTMLTextAreaElement>(null)
  const [query, setQuery] = useState<string | null>(null)
  const [index, setIndex] = useState(0)
  const rawOptions = useReferenceSearch(query)
  const options = rawOptions.filter(
    (o) => !references.some((r) => r.kind === o.kind && r.id === o.id),
  )
  const groups = groupReferenceOptions(options)
  const atCap = references.length >= maxReferences
  const flat = atCap ? [] : groups.flatMap((g) => g.options)
  const open = query !== null && flat.length > 0

  function updateQuery(text: string, caret: number) {
    const before = text.slice(0, caret)
    const match = mentionRe.exec(before)
    if (!match) {
      setQuery(null)
      return
    }
    setIndex(0)
    setQuery(match[2].toLowerCase())
  }

  function clearToken(): boolean {
    const el = inputRef.current
    if (!el) return false
    const caret = el.selectionStart ?? value.length
    const before = value.slice(0, caret)
    const match = mentionRe.exec(before)
    if (!match) return false
    const start = caret - match[2].length - 1
    onChange(value.slice(0, start) + value.slice(caret))
    setQuery(null)
    requestAnimationFrame(() => el.setSelectionRange(start, start))
    return true
  }

  function pick(option: { kind: Reference['kind']; id: string; name: string }) {
    if (atCap) return
    if (!clearToken()) return
    onReferences(addReference(references, option))
  }

  function remove(kind: Reference['kind'], id: string) {
    onReferences(removeReference(references, kind, id))
  }

  function handleKeyDown(e: KeyboardEvent<HTMLTextAreaElement>) {
    if (!open) return
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      setIndex((i) => (i + 1) % flat.length)
      return
    }
    if (e.key === 'ArrowUp') {
      e.preventDefault()
      setIndex((i) => (i - 1 + flat.length) % flat.length)
      return
    }
    if (e.key === 'Enter' || e.key === 'Tab') {
      e.preventDefault()
      pick(flat[Math.min(index, flat.length - 1)])
      return
    }
    if (e.key === 'Escape') {
      e.preventDefault()
      setQuery(null)
    }
  }

  return (
    <div className="space-y-2">
      <div className="relative">
        {open && (
          <div className="absolute bottom-full left-0 z-10 mb-1 max-h-72 w-72 overflow-y-auto rounded-lg border border-border bg-popover py-1 shadow-lg">
            {groups.map((group) => (
              <div key={group.kind}>
                <div className="px-3 pt-1.5 pb-0.5 text-[10px] font-semibold tracking-wider text-muted-foreground uppercase">
                  {referenceKindLabel[group.kind]}
                </div>
                {group.options.map((o) => {
                  const i = flat.findIndex((f) => f.kind === o.kind && f.id === o.id)
                  return (
                    <button
                      key={`${o.kind}-${o.id}`}
                      type="button"
                      onMouseDown={(e) => e.preventDefault()}
                      onClick={() => pick(o)}
                      className={
                        i === index
                          ? 'block w-full truncate px-3 py-1.5 text-left text-sm bg-muted text-foreground'
                          : 'block w-full truncate px-3 py-1.5 text-left text-sm text-foreground hover:bg-muted/60'
                      }
                    >
                      {o.name}
                    </button>
                  )
                })}
              </div>
            ))}
          </div>
        )}
        <Textarea
          ref={inputRef}
          value={value}
          onChange={(e) => {
            onChange(e.target.value)
            updateQuery(e.target.value, e.target.selectionStart ?? e.target.value.length)
          }}
          onSelect={(e) => {
            const el = e.currentTarget
            updateQuery(el.value, el.selectionStart ?? el.value.length)
          }}
          onKeyDown={handleKeyDown}
          {...textareaProps}
        />
      </div>
      {references.length > 0 && (
        <div className="flex flex-wrap gap-2">
          {references.map((r) => (
            <span
              key={`${r.kind}-${r.id}`}
              className="inline-flex max-w-56 items-center gap-1.5 rounded-lg border border-border bg-muted/30 py-1 pr-1.5 pl-2 text-xs"
            >
              <span className="truncate">
                {referenceKindLabel[r.kind].replace(/s$/, '')}: {r.name}
              </span>
              <button
                type="button"
                onClick={() => remove(r.kind, r.id)}
                aria-label={`Remove ${r.name} reference`}
                className="flex size-3.5 shrink-0 items-center justify-center rounded-full text-muted-foreground hover:text-foreground"
              >
                <HugeiconsIcon icon={Cancel01Icon} className="size-3" />
              </button>
            </span>
          ))}
        </div>
      )}
    </div>
  )
}
