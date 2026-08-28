import { useMemo } from 'react'
import hljs from 'highlight.js'
import ReactMarkdown from 'react-markdown'
import rehypeHighlight from 'rehype-highlight'
import remarkGfm from 'remark-gfm'
import { codeLanguageOf } from './missions/filePreviewKind'
import 'highlight.js/styles/github-dark.css'

// FileCodeBlock highlights plain (non-markdown) text/code files
// directly with highlight.js — hljs escapes the source itself before
// wrapping it in span tags, the same trust model rehype-highlight
// already uses for markdown, so the resulting HTML is safe to inject.
// A line-number gutter runs alongside it, GitHub-style: one row per
// source line, select-none so copying the code never grabs the numbers.
// Shared by missions/FileViewer.tsx and AttachmentViewer.tsx — named
// distinctly from the top-level CodeBlock.tsx (shiki, for chat markdown
// `pre` blocks), which this is unrelated to.
export function FileCodeBlock({ code, path }: { code: string; path: string }) {
  const html = useMemo(() => {
    const lang = codeLanguageOf(path)
    const result = lang
      ? hljs.highlight(code, { language: lang, ignoreIllegals: true })
      : hljs.highlightAuto(code)
    return result.value
  }, [code, path])
  const lineCount = useMemo(() => code.split('\n').length, [code])
  return (
    <div className="flex overflow-x-auto p-3 font-mono text-xs leading-5">
      <div aria-hidden="true" className="select-none pr-3 text-right text-muted-foreground">
        {Array.from({ length: lineCount }, (_, i) => (
          <div key={i}>{i + 1}</div>
        ))}
      </div>
      <pre className="min-w-0 flex-1 font-mono text-xs leading-5">
        <code
          className="hljs !bg-transparent !p-0 font-mono text-xs leading-5"
          // eslint-disable-next-line react/no-danger -- hljs escapes `code` itself; see FileCodeBlock comment above.
          dangerouslySetInnerHTML={{ __html: html }}
        />
      </pre>
    </div>
  )
}

// FileMarkdownBlock renders markdown text, or its raw source through
// FileCodeBlock when `raw` is toggled (the Source/Rendered switch).
export function FileMarkdownBlock({ text, raw }: { text: string; raw: boolean }) {
  if (raw) return <FileCodeBlock code={text} path="file.md" />
  return (
    <div className="prose prose-sm max-w-none p-3 dark:prose-invert prose-pre:bg-zinc-900">
      <ReactMarkdown remarkPlugins={[remarkGfm]} rehypePlugins={[rehypeHighlight]}>
        {text}
      </ReactMarkdown>
    </div>
  )
}
