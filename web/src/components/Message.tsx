import ReactMarkdown from 'react-markdown'
import rehypeHighlight from 'rehype-highlight'
import { Badge } from './catalyst/badge'
import type { AssistantState } from '../lib/chat'
import 'highlight.js/styles/github-dark.css'

export function UserMessage({ text }: { text: string }) {
  return (
    <div className="flex justify-end">
      <div className="max-w-2xl rounded-2xl bg-blue-600 px-4 py-2.5 text-sm/6 whitespace-pre-wrap text-white">
        {text}
      </div>
    </div>
  )
}

export function AssistantMessage({ msg }: { msg: AssistantState }) {
  const tokens = msg.meta?.usage
    ? `${msg.meta.usage.input_tokens}→${msg.meta.usage.output_tokens} tok`
    : null
  return (
    <div className="flex flex-col items-start gap-2">
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
        <Badge key={i} color="amber" data-testid="notice">
          {n}
        </Badge>
      ))}
      {msg.error && (
        <Badge color="red" data-testid="error">
          {msg.error}
        </Badge>
      )}
      {!msg.streaming && msg.meta?.provider && (
        <div className="flex gap-1.5" data-testid="meta-badge">
          <Badge color="zinc">{msg.meta.provider}</Badge>
          <Badge color="zinc">{msg.meta.model}</Badge>
          {tokens && <Badge color="zinc">{tokens}</Badge>}
        </div>
      )}
    </div>
  )
}
