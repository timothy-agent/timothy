import { ArrowUp01Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useEffect, useRef } from 'react'
import { CategoryPicker } from './CategoryPicker'

// Composer is the one message box: the chat page's docked input and
// the home page's hero input are the same component so behavior
// (autogrow, Enter-to-send, category pill) never drifts.
export function Composer({
  draft,
  onDraft,
  onSend,
  category,
  onCategory,
  disabled = false,
  autoFocus = false,
  placeholder = 'Message Timothy…',
}: {
  draft: string
  onDraft: (v: string) => void
  onSend: () => void
  category: string
  onCategory: (c: string) => void
  disabled?: boolean
  autoFocus?: boolean
  placeholder?: string
}) {
  const inputRef = useRef<HTMLTextAreaElement>(null)

  // Auto-grow up to a cap, then scroll inside. Runs on every draft
  // change so programmatic clears (post-send) shrink it back.
  useEffect(() => {
    const el = inputRef.current
    if (!el) return
    el.style.height = 'auto'
    el.style.height = `${Math.min(el.scrollHeight, 200)}px`
  }, [draft])

  return (
    <div className="rounded-2xl border border-zinc-950/10 bg-white shadow-sm transition focus-within:border-blue-500/50 focus-within:ring-4 focus-within:ring-blue-500/10 dark:border-white/10 dark:bg-zinc-800/60 dark:focus-within:border-blue-400/40">
      <textarea
        ref={inputRef}
        aria-label="Message"
        rows={1}
        value={draft}
        autoFocus={autoFocus}
        placeholder={placeholder}
        className="max-h-50 w-full resize-none bg-transparent px-4 pt-3.5 pb-1.5 text-base/6 text-zinc-900 outline-none placeholder:text-zinc-400 sm:text-sm/6 dark:text-white dark:placeholder:text-zinc-500"
        onChange={(e) => onDraft(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter' && !e.shiftKey) {
            e.preventDefault()
            onSend()
          }
        }}
      />
      <div className="flex items-center justify-between gap-2 px-2.5 pb-2.5">
        <CategoryPicker value={category} onChange={onCategory} />
        <button
          type="button"
          onClick={onSend}
          aria-label="Send"
          disabled={disabled || draft.trim() === ''}
          className="flex size-9 shrink-0 items-center justify-center rounded-full bg-blue-600 text-white transition hover:bg-blue-500 disabled:bg-zinc-200 disabled:text-zinc-400 dark:disabled:bg-zinc-700 dark:disabled:text-zinc-500"
        >
          <HugeiconsIcon icon={ArrowUp01Icon} className="size-4" />
        </button>
      </div>
    </div>
  )
}
