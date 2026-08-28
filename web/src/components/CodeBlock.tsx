import type { ReactNode } from 'react'
import { Children, isValidElement, useEffect, useState } from 'react'
import type { ExtraProps } from 'react-markdown'
import bashLogo from '../assets/langs/bash.svg'
import cLogo from '../assets/langs/c.svg'
import cplusplusLogo from '../assets/langs/cplusplus.svg'
import csharpLogo from '../assets/langs/csharp.svg'
import css3Logo from '../assets/langs/css3.svg'
import dockerLogo from '../assets/langs/docker.svg'
import goLogo from '../assets/langs/go.svg'
import html5Logo from '../assets/langs/html5.svg'
import javaLogo from '../assets/langs/java.svg'
import javascriptLogo from '../assets/langs/javascript.svg'
import jsonLogo from '../assets/langs/json.svg'
import kotlinLogo from '../assets/langs/kotlin.svg'
import markdownLogo from '../assets/langs/markdown.svg'
import mysqlLogo from '../assets/langs/mysql.svg'
import phpLogo from '../assets/langs/php.svg'
import pythonLogo from '../assets/langs/python.svg'
import rubyLogo from '../assets/langs/ruby.svg'
import rustLogo from '../assets/langs/rust.svg'
import swiftLogo from '../assets/langs/swift.svg'
import typescriptLogo from '../assets/langs/typescript.svg'
import yamlLogo from '../assets/langs/yaml.svg'
import { CopyButton } from './Message'

// GitHub linguist colors for common languages, small dot next to the
// language name in the header. Unrecognized languages fall back to gray.
const LANGUAGE_COLORS: Record<string, string> = {
  python: '#3572A5',
  typescript: '#3178c6',
  tsx: '#3178c6',
  javascript: '#f1e05a',
  jsx: '#f1e05a',
  go: '#00ADD8',
  rust: '#dea584',
  json: '#7f7f7f',
  bash: '#89e051',
  sh: '#89e051',
  shell: '#89e051',
  sql: '#e38c00',
  yaml: '#cb171e',
  yml: '#cb171e',
  html: '#e34c26',
  css: '#563d7c',
  markdown: '#083fa1',
  java: '#b07219',
  c: '#555555',
  cpp: '#f34b7d',
  csharp: '#178600',
  ruby: '#701516',
  php: '#4F5D95',
  swift: '#F05138',
  kotlin: '#A97BFF',
  dockerfile: '#384d54',
  makefile: '#427819',
  diff: '#4a4a4a',
  toml: '#9c4221',
  graphql: '#e10098',
}

// Official per-language logos, vendored from devicon
// (https://github.com/devicons/devicon, MIT licensed) as static SVGs
// under ../assets/langs/ — hugeicons' stroke-rounded set has no real
// per-language marks (its "PhpIcon" etc. are generic glyphs, not the
// actual PHP logo), so this replaces that with the real thing.
// jsx/tsx share their base language's logo (same language, JSX
// syntax); sql uses the mysql mark as the generic "database" stand-in
// (devicon has no neutral/vendor-agnostic sql icon). Any language not
// listed below (diff, toml, makefile, graphql, ...) falls back to the
// colored dot above.
const LANGUAGE_LOGOS: Record<string, string> = {
  javascript: javascriptLogo,
  jsx: javascriptLogo,
  typescript: typescriptLogo,
  tsx: typescriptLogo,
  python: pythonLogo,
  java: javaLogo,
  go: goLogo,
  rust: rustLogo,
  php: phpLogo,
  ruby: rubyLogo,
  kotlin: kotlinLogo,
  swift: swiftLogo,
  csharp: csharpLogo,
  c: cLogo,
  cpp: cplusplusLogo,
  html: html5Logo,
  css: css3Logo,
  bash: bashLogo,
  sh: bashLogo,
  shell: bashLogo,
  sql: mysqlLogo,
  yaml: yamlLogo,
  yml: yamlLogo,
  json: jsonLogo,
  markdown: markdownLogo,
  dockerfile: dockerLogo,
}

// Logos whose devicon artwork is dark (near-black fills or a
// black-to-white gradient) and disappears against the code block's
// dark zinc-900 chrome: bash's mark is solid #293138, markdown's has
// no explicit fill (defaults to black), json's gradient runs to pure
// black at one end. Devicon ships no currentColor/plain variant for
// these, so instead of the raw <img> they get a small light chip
// behind them in dark mode only (light mode already has a light
// background, so no chip needed there).
const DARK_MODE_NEEDS_CHIP = new Set(['bash', 'sh', 'shell', 'markdown', 'json'])

// shiki/langs (bundledLanguages/bundledLanguagesAlias: id/alias ->
// lazy grammar loader, one entry per shiki-supported language) is
// dynamically imported and cached module-scope, same as the
// highlighter itself — it's only the id/loader lookup table, not any
// grammar code, but keeping it out of the eager import graph means the
// main chat bundle carries zero shiki bytes until a code block mounts.
type LangIndex = Awaited<typeof import('shiki/langs')>
let langIndexPromise: Promise<LangIndex> | undefined

function loadLangIndex(): Promise<LangIndex> {
  if (!langIndexPromise) langIndexPromise = import('shiki/langs')
  return langIndexPromise
}

// Resolves a fence's language tag to a shiki bundled language id, or
// undefined if shiki ships nothing for it. Checks the canonical id set
// first (bundledLanguages), then aliases (bundledLanguagesAlias, e.g.
// sh/shell -> bash, yml -> yaml, cs -> csharp) — shiki bundles 300+
// languages this way, each behind its own lazy loader, so this covers
// every language shiki can highlight, not a curated subset.
export async function resolveShikiLangId(lang: string): Promise<string | undefined> {
  const { bundledLanguages, bundledLanguagesAlias } = await loadLangIndex()
  const key = lang.toLowerCase()
  if (key in bundledLanguages || key in bundledLanguagesAlias) return key
  return undefined
}

// Heuristic language detector, used only when a fence has no
// language-xxx tag (models frequently emit untagged fences). Each
// pattern is either a "strong" anchor (its own hit is enough to
// accept the language) or a "weak" keyword (needs a second hit,
// distinct pattern or otherwise, to clear the confidence bar). Scans
// only the first 2000 chars so it stays cheap re-run on every
// streaming render.
type DetectPattern = { re: RegExp; strong?: boolean }
export const DETECT_PATTERNS: Record<string, DetectPattern[]> = {
  java: [
    { re: /\bpublic\s+class\b/, strong: true },
    { re: /\bpublic\s+static\s+void\s+main\b/, strong: true },
    { re: /\bSystem\.out\.(println|print|printf)\b/, strong: true },
    { re: /\bprivate\s+static\s+\w+\s+\w+\(/ },
    { re: /;\s*$/m },
  ],
  python: [
    { re: /^def\s+\w+\(/m, strong: true },
    { re: /^import\s+\w+/m },
    { re: /^from\s+\w+\s+import\b/m },
    { re: /\bself\./ },
    { re: /:\s*\n\s+\S/ },
  ],
  go: [
    { re: /^package\s+\w+/m, strong: true },
    { re: /^func\s+\w*\(/m, strong: true },
    { re: /:=/ },
  ],
  cpp: [
    { re: /#include\s*<\w+>/, strong: true },
    { re: /\bstd::\w+/, strong: true },
    { re: /\busing\s+namespace\s+std\b/ },
    { re: /\bcout\s*<</ },
  ],
  c: [
    { re: /#include\s*<[\w.]+\.h>/, strong: true },
    { re: /\bprintf\s*\(/ },
    { re: /\bint\s+main\s*\(/ },
  ],
  rust: [
    { re: /\bfn\s+\w+\(/, strong: true },
    { re: /\blet\s+mut\b/ },
    { re: /::\w+/ },
  ],
  typescript: [
    { re: /\binterface\s+\w+/, strong: true },
    { re: /:\s*(string|number|boolean|void|any)\b/, strong: true },
    { re: /\w+\??:\s*\w+(\[\])?\s*[,)]/ },
    { re: /=>/ },
  ],
  javascript: [
    { re: /\bconst\s+\w+\s*=/ },
    { re: /\bmodule\.exports\b/ },
    { re: /\brequire\(/ },
    { re: /=>/ },
    { re: /\bfunction\s+\w*\(/ },
  ],
  json: [{ re: /^\s*[{[]/, strong: true }, { re: /"\w[\w-]*"\s*:/ }],
  yaml: [{ re: /^[\w.-]+:\s/m }, { re: /^\s*-\s+\w/m }, { re: /^[\w.-]+:\s*\n/m }],
  bash: [
    { re: /^#!.*\b(bash|sh)\b/, strong: true },
    { re: /^\$\s+\S/m },
    { re: /&&/ },
    { re: /\becho\s+/ },
  ],
  sql: [
    { re: /\b(SELECT|INSERT\s+INTO|CREATE\s+TABLE|UPDATE|DELETE\s+FROM)\b/i, strong: true },
    { re: /\bFROM\s+\w+/i },
    { re: /\bWHERE\b/i },
  ],
  html: [
    { re: /^<!doctype html/i, strong: true },
    { re: /<\/?(div|html|body|span|head)\b/i },
  ],
  css: [{ re: /[.#]?[\w-]+\s*\{[^}]*:[^}]+\}/ }, { re: /^@(media|import)\b/m }],
  markdown: [{ re: /^#{1,6}\s+\S/m }, { re: /^[-*]\s+\S/m }, { re: /\[.+\]\(.+\)/ }],
  dockerfile: [
    { re: /^FROM\s+\S+/m, strong: true },
    { re: /^RUN\s+\S/m },
    { re: /^(COPY|WORKDIR|CMD|ENTRYPOINT)\s/m },
  ],
  toml: [{ re: /^\[[\w.-]+\]\s*$/m, strong: true }, { re: /^[\w.-]+\s*=\s*/m }],
  diff: [
    { re: /^diff --git\b/m, strong: true },
    { re: /^@@ .+ @@/m, strong: true },
    { re: /^[+-]{3} /m },
  ],
}

const MIN_CONFIDENCE = 2
const DETECT_SCAN_LIMIT = 2000

// Scores every candidate language against `text` and returns the
// highest-scoring one that clears MIN_CONFIDENCE, or undefined if
// nothing does (stays plain text rather than guessing wrong).
export function detectLanguage(text: string): string | undefined {
  const sample = text.slice(0, DETECT_SCAN_LIMIT)
  let best: string | undefined
  let bestScore = 0
  for (const [lang, patterns] of Object.entries(DETECT_PATTERNS)) {
    let score = 0
    for (const { re, strong } of patterns) {
      if (re.test(sample)) score += strong ? 2 : 1
    }
    if (score > bestScore) {
      best = lang
      bestScore = score
    }
  }
  return bestScore >= MIN_CONFIDENCE ? best : undefined
}

// Singleton highlighter promise, module-scoped: every CodeBlock mount
// awaits the same load rather than re-initializing shiki per block.
// Loaded via dynamic import so the chat hot path only pays for shiki
// once a code fence actually renders — and loaded with NO languages:
// each block's language grammar is fetched separately and on demand
// (see ensureLangLoaded below), so a fence never pulls in grammars for
// languages nobody used in this chat.
type ShikiHighlighter = Awaited<ReturnType<typeof import('shiki/core').createHighlighterCore>>
let highlighterPromise: Promise<ShikiHighlighter> | undefined

function loadHighlighter(): Promise<ShikiHighlighter> {
  if (!highlighterPromise) {
    highlighterPromise = (async () => {
      const [{ createHighlighterCore }, { createJavaScriptRegexEngine }, githubDark, githubLight] =
        await Promise.all([
          import('shiki/core'),
          import('shiki/engine/javascript'),
          import('shiki/themes/github-dark.mjs'),
          import('shiki/themes/github-light.mjs'),
        ])
      return createHighlighterCore({
        themes: [githubDark.default, githubLight.default],
        langs: [],
        engine: createJavaScriptRegexEngine(),
      })
    })()
  }
  return highlighterPromise
}

// Per-language load cache, module-scoped like the highlighter promise:
// a language grammar is fetched and registered into the highlighter at
// most once per session, no matter how many blocks use it. Returns
// undefined for a lang shiki doesn't bundle (resolveShikiLangId already
// filters those out before this is called) or if the fetch/registration
// fails, so a bad chunk load degrades to plain text instead of wedging
// the block.
const langLoadPromises = new Map<string, Promise<void>>()

async function ensureLangLoaded(highlighter: ShikiHighlighter, langId: string): Promise<boolean> {
  if (highlighter.getLoadedLanguages().includes(langId)) return true
  let promise = langLoadPromises.get(langId)
  if (!promise) {
    promise = (async () => {
      const { bundledLanguages, bundledLanguagesAlias } = await loadLangIndex()
      const loader = bundledLanguages[langId as keyof typeof bundledLanguages] ?? bundledLanguagesAlias[langId]
      if (!loader) return
      await highlighter.loadLanguage(loader)
    })()
    langLoadPromises.set(langId, promise)
  }
  try {
    await promise
  } catch {
    langLoadPromises.delete(langId)
    return false
  }
  return highlighter.getLoadedLanguages().includes(langId)
}

// Recursively pulls raw text out of a react-markdown children tree,
// used both for the line count and the copy button's clipboard text.
export function extractText(node: ReactNode): string {
  if (typeof node === 'string' || typeof node === 'number') return String(node)
  if (Array.isArray(node)) return node.map(extractText).join('')
  if (isValidElement<{ children?: ReactNode }>(node)) return extractText(node.props.children)
  return ''
}

// hast Element shape as passed via react-markdown's `node` prop,
// declared locally to avoid a direct hast-types dependency.
type HastElement = {
  type: 'element'
  tagName: string
  properties?: { className?: unknown }
  children?: HastElement[]
}

function findLanguageClass(node: HastElement | undefined): string | undefined {
  for (const child of node?.children ?? []) {
    if (child.type !== 'element') continue
    const classes = child.properties?.className
    const list = Array.isArray(classes) ? classes : typeof classes === 'string' ? [classes] : []
    const match = list.map(String).find((c) => c.startsWith('language-'))
    if (match) return match.slice('language-'.length)
    const nested = findLanguageClass(child)
    if (nested) return nested
  }
  return undefined
}

// Fallback when the hast node isn't available: reads the `language-xxx`
// class straight off the rendered `<code>` child element.
function languageFromChildren(children: ReactNode): string | undefined {
  let found: string | undefined
  Children.forEach(children, (child) => {
    if (found) return
    if (isValidElement<{ className?: string }>(child) && typeof child.props.className === 'string') {
      const match = /language-(\S+)/.exec(child.props.className)
      if (match) found = match[1]
    }
  })
  return found
}

// Highlights `text` with shiki: resolves the language against shiki's
// full bundled set, loads its grammar on demand (cached, so a repeated
// language across blocks or re-renders never re-fetches), then
// highlights. Returns undefined — plain text stays up — while any of
// that is pending, when the language isn't one shiki bundles, or if
// loading/highlighting fails for any reason (a bad chunk load degrades
// to plain text rather than wedging the block).
function useHighlightedHtml(text: string, language: string | undefined): string | undefined {
  const [html, setHtml] = useState<string | undefined>(undefined)

  useEffect(() => {
    setHtml(undefined)
    if (!language) return
    const lang = language.toLowerCase()
    let stale = false
    ;(async () => {
      const langId = await resolveShikiLangId(lang)
      if (!langId || stale) return
      const highlighter = await loadHighlighter()
      if (stale) return
      const loaded = await ensureLangLoaded(highlighter, langId)
      if (!loaded || stale) return
      const out = highlighter.codeToHtml(text, {
        lang: langId,
        themes: { dark: 'github-dark', light: 'github-light' },
        defaultColor: false,
      })
      if (!stale) setHtml(out)
    })().catch(() => {
      if (!stale) setHtml(undefined)
    })
    return () => {
      stale = true
    }
  }, [text, language])

  return html
}

// CodeBlock replaces react-markdown's default `pre` for fenced code
// blocks: a GitHub-style header (language icon or colored dot + name,
// copy button) above a line-numbered, horizontally scrollable code
// area, themed with the rest of the app via the .dark class (index.css
// picks --shiki-dark there). Highlighting runs async via shiki
// (react-markdown can't run async rehype plugins); until it resolves,
// or for an unrecognized language, the plain text renders so streaming
// never blocks on it.
export function CodeBlock({ children, node }: { children?: ReactNode } & ExtraProps) {
  const taggedLanguage =
    findLanguageClass(node as unknown as HastElement | undefined) ?? languageFromChildren(children)
  const text = extractText(children).replace(/\n$/, '')
  // Only guess when the fence carries no language tag at all — a tagged
  // fence (even one with an unrecognized tag) keeps it verbatim.
  const language = taggedLanguage ?? detectLanguage(text)
  const lineCount = text === '' ? 1 : text.split('\n').length
  const langKey = language?.toLowerCase()
  const color = langKey ? LANGUAGE_COLORS[langKey] : undefined
  const logo = langKey ? LANGUAGE_LOGOS[langKey] : undefined
  const needsChip = langKey ? DARK_MODE_NEEDS_CHIP.has(langKey) : false
  const html = useHighlightedHtml(text, language)

  return (
    <div className="not-prose my-4 overflow-hidden rounded-lg border border-zinc-200 bg-zinc-50 dark:border-zinc-800 dark:bg-zinc-900">
      <div className="flex items-center justify-between border-b border-zinc-200 bg-zinc-100 px-3 py-1.5 dark:border-zinc-800 dark:bg-zinc-800/60">
        <span className="flex items-center gap-1.5 text-xs text-zinc-500 dark:text-zinc-400">
          {logo ? (
            <span
              className={needsChip ? 'flex items-center rounded-[3px] dark:bg-white/95 dark:p-[1px]' : 'flex items-center'}
            >
              <img src={logo} alt="" className="size-3.5" />
            </span>
          ) : (
            <span
              className="size-2 rounded-full"
              style={{ backgroundColor: color ?? '#6b7280' }}
              aria-hidden="true"
            />
          )}
          {language ?? 'text'}
        </span>
        <CopyButton text={text} label="Copy code" alwaysVisible />
      </div>
      <div className="flex overflow-x-auto">
        <div
          className="sticky left-0 shrink-0 select-none bg-zinc-50 px-3 py-3 text-right font-mono text-sm leading-relaxed text-zinc-400 dark:bg-zinc-900 dark:text-zinc-600"
          aria-hidden="true"
        >
          {Array.from({ length: lineCount }, (_, i) => (
            <div key={i}>{i + 1}</div>
          ))}
        </div>
        {html ? (
          <div
            className="shiki-container min-w-0 flex-1 py-3 pr-4 font-mono text-sm leading-relaxed"
            data-testid="shiki-html"
            dangerouslySetInnerHTML={{ __html: html }}
          />
        ) : (
          <pre className="min-w-0 flex-1 py-3 pr-4 font-mono text-sm leading-relaxed text-zinc-900 dark:text-zinc-100">
            {text}
          </pre>
        )}
      </div>
    </div>
  )
}
