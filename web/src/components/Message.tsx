import { Copy01Icon, Tick02Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useEffect, useRef, useState } from 'react'
import ReactMarkdown from 'react-markdown'
import rehypeHighlight from 'rehype-highlight'
import { Badge } from './ui/badge'
import type { AssistantState } from '../lib/chat'
import 'highlight.js/styles/github-dark.css'

// CopyButton copies a message's raw text; the check confirms briefly.
function CopyButton({ text, label }: { text: string; label: string }) {
  const [copied, setCopied] = useState(false)
  const timer = useRef<number | undefined>(undefined)
  useEffect(() => () => window.clearTimeout(timer.current), [])
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text)
      setCopied(true)
      window.clearTimeout(timer.current)
      timer.current = window.setTimeout(() => setCopied(false), 2000)
    } catch {
      // Clipboard unavailable (permissions, insecure context): the
      // button simply does nothing rather than throwing.
    }
  }
  return (
    <button
      type="button"
      aria-label={label}
      data-testid="copy-button"
      data-copied={copied}
      onClick={() => void copy()}
      className="rounded p-1 text-muted-foreground opacity-0 transition group-hover/message:opacity-100 hover:bg-accent hover:text-foreground focus-visible:opacity-100"
    >
      <HugeiconsIcon icon={copied ? Tick02Icon : Copy01Icon} className="size-3.5" />
    </button>
  )
}

export function UserMessage({ text }: { text: string }) {
  return (
    <div className="group/message flex items-end justify-end gap-1">
      <CopyButton text={text} label="Copy message" />
      <div className="max-w-2xl rounded-2xl bg-blue-600 px-4 py-2.5 text-sm/6 whitespace-pre-wrap text-white">
        {text}
      </div>
    </div>
  )
}

// CompactionDivider marks where older messages were summarized away
// from the model's context. The UI replay still shows everything above
// it — only the model forgets, and the divider says so.
export function CompactionDivider({ text }: { text: string }) {
  return (
    <div className="flex items-center gap-3" data-testid="compaction-divider">
      <div className="h-px flex-1 bg-zinc-200 dark:bg-zinc-700" />
      <span className="text-xs text-zinc-400 dark:text-zinc-500">{text}</span>
      <div className="h-px flex-1 bg-zinc-200 dark:bg-zinc-700" />
    </div>
  )
}

// InterruptedMessage renders a turn that never completed: the partial
// answer plus an honest marker.
export function InterruptedMessage({ text }: { text: string }) {
  return (
    <div className="group/message flex flex-col items-start gap-2" data-testid="interrupted">
      <div className="prose prose-sm max-w-3xl dark:prose-invert prose-pre:bg-zinc-900">
        <ReactMarkdown rehypePlugins={[rehypeHighlight]}>{text}</ReactMarkdown>
      </div>
      <div className="flex items-center gap-1.5">
        <Badge
          variant="outline"
          className="border-amber-500/40 bg-amber-500/10 text-amber-600 dark:text-amber-400"
        >
          interrupted
        </Badge>
        <CopyButton text={text} label="Copy partial message" />
      </div>
    </div>
  )
}

export function AssistantMessage({ msg }: { msg: AssistantState }) {
  const tokens = msg.meta?.usage
    ? `${msg.meta.usage.input_tokens}→${msg.meta.usage.output_tokens} tok`
    : null
  return (
    <div className="group/message flex flex-col items-start gap-2">
      {msg.reasoning !== '' && (
        <details className="w-full max-w-3xl rounded-lg border border-zinc-200 px-3 py-2 text-xs text-zinc-500 dark:border-zinc-700 dark:text-zinc-400">
          <summary className="cursor-pointer select-none">Reasoning</summary>
          <div className="mt-2 whitespace-pre-wrap">{msg.reasoning}</div>
        </details>
      )}

      <div className="prose prose-sm max-w-3xl dark:prose-invert prose-pre:bg-zinc-900">
        <ReactMarkdown rehypePlugins={[rehypeHighlight]}>{msg.text}</ReactMarkdown>
        {msg.streaming && <span className="animate-pulse">▍</span>}
      </div>

      {msg.notices.map((n, i) => (
        <Badge
          key={i}
          variant="outline"
          className="border-amber-500/40 bg-amber-500/10 text-amber-600 dark:text-amber-400"
          data-testid="notice"
        >
          {n}
        </Badge>
      ))}
      {msg.error && (
        <Badge variant="destructive" data-testid="error">
          {msg.error}
        </Badge>
      )}
      {!msg.streaming && (Boolean(msg.meta?.provider) || msg.text !== '') && (
        <div className="flex items-center gap-1.5">
          {msg.meta?.provider && (
            <div className="flex gap-1.5" data-testid="meta-badge">
              <Badge variant="secondary">{msg.meta.provider}</Badge>
              <Badge variant="secondary">{msg.meta.model}</Badge>
              {tokens && <Badge variant="secondary">{tokens}</Badge>}
            </div>
          )}
          {msg.text !== '' && <CopyButton text={msg.text} label="Copy reply" />}
        </div>
      )}
    </div>
  )
}
