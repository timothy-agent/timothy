import { useEffect, useId, useState } from 'react'
import { CopyButton } from './Message'

// Lazy-loaded mermaid singleton, same pattern as CodeBlock.tsx's shiki
// highlighter: the main bundle carries zero mermaid bytes until a
// ```mermaid fence actually renders. mermaid.initialize is idempotent
// and cheap to re-call on every theme flip (see useMermaidTheme below).
type Mermaid = typeof import('mermaid').default
let mermaidPromise: Promise<Mermaid> | undefined

function loadMermaid(): Promise<Mermaid> {
  if (!mermaidPromise) {
    mermaidPromise = import('mermaid').then((m) => {
      m.default.initialize({ startOnLoad: false, securityLevel: 'strict' })
      return m.default
    })
  }
  return mermaidPromise
}

// Tracks the .dark class on <html> (lib/theme.ts's applyTheme target)
// so a diagram re-renders in the matching mermaid theme when the app
// theme flips, without polling.
function useIsDarkTheme(): boolean {
  const [isDark, setIsDark] = useState(() => document.documentElement.classList.contains('dark'))
  useEffect(() => {
    const observer = new MutationObserver(() =>
      setIsDark(document.documentElement.classList.contains('dark')),
    )
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ['class'] })
    return () => observer.disconnect()
  }, [])
  return isDark
}

// MermaidBlock renders a ```mermaid fence's source as an SVG diagram,
// with a toggle back to the raw source. Falls back to the source view
// with no crash when mermaid can't parse it (invalid syntax, or a
// language tag that isn't actually mermaid).
export function MermaidBlock({ code }: { code: string }) {
  const id = useId().replace(/:/g, '-')
  const isDark = useIsDarkTheme()
  const [svg, setSvg] = useState<string | undefined>(undefined)
  const [failed, setFailed] = useState(false)
  const [showSource, setShowSource] = useState(false)

  useEffect(() => {
    let stale = false
    setSvg(undefined)
    setFailed(false)
    ;(async () => {
      const mermaid = await loadMermaid()
      if (stale) return
      mermaid.initialize({
        startOnLoad: false,
        securityLevel: 'strict',
        theme: isDark ? 'dark' : 'default',
      })
      const { svg: rendered } = await mermaid.render(`mermaid-${id}`, code)
      if (!stale) setSvg(rendered)
    })().catch(() => {
      if (!stale) setFailed(true)
    })
    return () => {
      stale = true
    }
  }, [code, id, isDark])

  if (failed || showSource) {
    return (
      <div className="not-prose my-4 overflow-hidden rounded-lg border border-zinc-200 bg-zinc-50 dark:border-zinc-800 dark:bg-zinc-900">
        <div className="flex items-center justify-between border-b border-zinc-200 bg-zinc-100 px-3 py-1.5 dark:border-zinc-800 dark:bg-zinc-800/60">
          <span className="text-xs text-zinc-500 dark:text-zinc-400">mermaid</span>
          <div className="flex items-center gap-1">
            {!failed && (
              <button
                type="button"
                onClick={() => setShowSource(false)}
                className="text-xs text-zinc-500 underline underline-offset-2 hover:text-zinc-700 dark:text-zinc-400 dark:hover:text-zinc-200"
              >
                View diagram
              </button>
            )}
            <CopyButton text={code} label="Copy source" alwaysVisible />
          </div>
        </div>
        <pre className="overflow-x-auto p-3 font-mono text-sm leading-relaxed text-zinc-900 dark:text-zinc-100">
          {code}
        </pre>
      </div>
    )
  }

  return (
    <div className="not-prose my-4 overflow-hidden rounded-lg border border-zinc-200 bg-zinc-50 dark:border-zinc-800 dark:bg-zinc-900">
      <div className="flex items-center justify-between border-b border-zinc-200 bg-zinc-100 px-3 py-1.5 dark:border-zinc-800 dark:bg-zinc-800/60">
        <span className="text-xs text-zinc-500 dark:text-zinc-400">mermaid</span>
        <div className="flex items-center gap-1">
          <button
            type="button"
            onClick={() => setShowSource(true)}
            className="text-xs text-zinc-500 underline underline-offset-2 hover:text-zinc-700 dark:text-zinc-400 dark:hover:text-zinc-200"
          >
            View source
          </button>
          <CopyButton text={code} label="Copy source" alwaysVisible />
        </div>
      </div>
      {svg ? (
        <div className="overflow-x-auto p-3" dangerouslySetInnerHTML={{ __html: svg }} />
      ) : (
        <pre className="overflow-x-auto p-3 font-mono text-sm leading-relaxed text-zinc-900 dark:text-zinc-100">
          {code}
        </pre>
      )}
    </div>
  )
}
