import type { ReactNode } from 'react'
import type { ExtraProps } from 'react-markdown'
import { extractText, findLanguageClass, type HastElement, languageFromChildren } from './CodeBlock'
import { MermaidBlock } from './MermaidBlock'

// MarkdownPre replaces react-markdown's default `pre` for sections that
// render plain (mission goal/explore/result notes, file previews) —
// unlike CodeBlock.tsx it does no shiki highlighting, it only detects a
// ```mermaid fence and hands it to MermaidBlock; anything else falls
// through to a plain <pre> unchanged.
export function MarkdownPre({ children, node }: { children?: ReactNode } & ExtraProps) {
  const language =
    findLanguageClass(node as unknown as HastElement | undefined) ?? languageFromChildren(children)
  if (language?.toLowerCase() === 'mermaid') {
    const text = extractText(children).replace(/\n$/, '')
    return <MermaidBlock code={text} />
  }
  return <pre>{children}</pre>
}
