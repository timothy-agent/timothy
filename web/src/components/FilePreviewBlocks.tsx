import { useMemo } from 'react'
import ReactMarkdown from 'react-markdown'
import { CodeBlock, detectLanguage, useHighlightedHtml } from './CodeBlock'
import { rehypePlugins, remarkPlugins } from '../lib/markdown'
import { codeLanguageOf } from './missions/filePreviewKind'

// FileCodeBlock highlights plain (non-markdown) text/code files with
// the same shiki highlighter and github-light/github-dark themes as
// chat's CodeBlock, so a file reads identically wherever it's shown.
// The language comes from the path (extension or basename), falling
// back to CodeBlock's content heuristic for extensionless files. A
// line-number gutter runs alongside it, GitHub-style: one row per
// source line, select-none so copying the code never grabs the
// numbers. Shared by missions/FileViewer.tsx and AttachmentViewer.tsx.
export function FileCodeBlock({ code, path }: { code: string; path: string }) {
  const language = useMemo(() => codeLanguageOf(path) ?? detectLanguage(code), [code, path])
  const html = useHighlightedHtml(code, language)
  const lineCount = useMemo(() => code.split('\n').length, [code])
  return (
    <div className="flex overflow-x-auto font-mono text-code leading-5">
      <div
        aria-hidden="true"
        className="sticky left-0 shrink-0 select-none bg-card px-3 py-3 text-right text-muted-foreground"
      >
        {Array.from({ length: lineCount }, (_, i) => (
          <div key={i}>{i + 1}</div>
        ))}
      </div>
      {html ? (
        <div
          className="shiki-container min-w-0 flex-1 p-3"
          data-testid="shiki-html"
          dangerouslySetInnerHTML={{ __html: html }}
        />
      ) : (
        <pre className="min-w-0 flex-1 p-3 text-foreground">{code}</pre>
      )}
    </div>
  )
}

// FileMarkdownBlock renders markdown text, or its raw source through
// FileCodeBlock when `raw` is toggled (the Source/Rendered switch).
export function FileMarkdownBlock({ text, raw }: { text: string; raw: boolean }) {
  if (raw) return <FileCodeBlock code={text} path="file.md" />
  return (
    <div className="prose max-w-none p-3 dark:prose-invert">
      <ReactMarkdown
        remarkPlugins={remarkPlugins}
        rehypePlugins={rehypePlugins}
        components={{ pre: CodeBlock }}
      >
        {text}
      </ReactMarkdown>
    </div>
  )
}
