import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import ReactMarkdown from 'react-markdown'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { CodeBlock, DETECT_PATTERNS, detectLanguage, extractText, resolveShikiLangId } from './CodeBlock'

afterEach(cleanup)

// Bundled-language ids this test double knows how to "load" — a small
// stand-in for shiki's real 300+ entry table, covering every id/alias
// the tests exercise plus a curated sample so the loadable-languages
// guard test below has something non-trivial to check against.
const KNOWN_LANG_IDS = [
  'typescript',
  'javascript',
  'tsx',
  'jsx',
  'python',
  'go',
  'rust',
  'json',
  'yaml',
  'bash',
  'sql',
  'html',
  'css',
  'markdown',
  'diff',
  'dockerfile',
  'toml',
  'java',
  'c',
  'cpp',
  'php',
]
const KNOWN_ALIASES: Record<string, string> = { sh: 'bash', shell: 'bash', yml: 'yaml' }

// Each loader carries the keys loading it should register as loaded
// (its own id, plus any alias it was also registered under) — mirrors
// shiki's real behavior where loading one grammar makes every alias of
// it immediately "loaded" too (verified against real shiki: loading
// bash's loader makes getLoadedLanguages() report bash/sh/shell/zsh).
type MockLoader = (() => Promise<[{ name: string }]>) & { keys: string[] }
function mockLoader(...keys: string[]): MockLoader {
  const loader = (() => Promise.resolve([{ name: keys[0] }])) as MockLoader
  loader.keys = keys
  return loader
}
const bundledLanguages = Object.fromEntries(
  KNOWN_LANG_IDS.map((id) => [
    id,
    mockLoader(id, ...Object.entries(KNOWN_ALIASES).filter(([, base]) => base === id).map(([alias]) => alias)),
  ]),
)
const bundledLanguagesAlias = Object.fromEntries(
  Object.entries(KNOWN_ALIASES).map(([alias, id]) => [alias, bundledLanguages[id]]),
)

// The highlighter double tracks its own loaded-language set, same
// contract as the real shiki HighlighterCore: loadLanguage registers a
// loader's grammar (its id and every alias it carries), getLoadedLanguages
// reports what's registered, and codeToHtml is the deterministic markup
// callers assert against — no dependency on shiki's real grammars.
const loadedLangs = new Set<string>(['plaintext'])
const codeToHtml = vi.fn(
  (code: string, opts: { lang: string }) =>
    `<pre class="shiki"><code><span class="line" data-lang="${opts.lang}">${code}</span></code></pre>`,
)
const loadLanguage = vi.fn(async (...loaders: MockLoader[]) => {
  for (const loader of loaders) {
    await loader()
    for (const key of loader.keys) loadedLangs.add(key)
  }
})
const getLoadedLanguages = vi.fn(() => [...loadedLangs])
const createHighlighterCore = vi.fn(async () => ({ codeToHtml, loadLanguage, getLoadedLanguages }))

// vi.mock() is hoisted above everything else in the module, so mock
// factories can't close over the plain `bundledLanguages` object above
// (it's declared with `const`, fine — hoisting only forbids referencing
// let/const *before* their declaration executes, and these factories
// only run when the module is actually imported, well after setup).
vi.mock('shiki/core', () => ({ createHighlighterCore }))
vi.mock('shiki/engine/javascript', () => ({ createJavaScriptRegexEngine: vi.fn(() => ({})) }))
vi.mock('shiki/themes/github-dark.mjs', () => ({ default: { name: 'github-dark' } }))
vi.mock('shiki/themes/github-light.mjs', () => ({ default: { name: 'github-light' } }))
vi.mock('shiki/langs', () => ({
  get bundledLanguages() {
    return bundledLanguages
  },
  get bundledLanguagesAlias() {
    return bundledLanguagesAlias
  },
}))

function renderMarkdown(text: string) {
  return render(<ReactMarkdown components={{ pre: CodeBlock }}>{text}</ReactMarkdown>)
}

describe('extractText', () => {
  it('flattens plain strings', () => {
    expect(extractText('hello')).toBe('hello')
  })

  it('flattens nested elements and arrays', () => {
    const tree = (
      <span>
        {'a'}
        <em>b</em>
        {['c', 'd']}
      </span>
    )
    expect(extractText(tree)).toBe('abcd')
  })

  it('returns empty string for null/undefined', () => {
    expect(extractText(null)).toBe('')
    expect(extractText(undefined)).toBe('')
  })
})

describe('resolveShikiLangId', () => {
  it('resolves a canonical bundled id', async () => {
    expect(await resolveShikiLangId('python')).toBe('python')
  })

  it('resolves an alias to its own key (shiki accepts aliases directly)', async () => {
    expect(await resolveShikiLangId('sh')).toBe('sh')
  })

  it('is case-insensitive', async () => {
    expect(await resolveShikiLangId('Python')).toBe('python')
  })

  it('returns undefined for a language shiki does not bundle', async () => {
    expect(await resolveShikiLangId('brainfuck')).toBeUndefined()
  })
})

describe('detectLanguage', () => {
  const cases: [string, string, string][] = [
    [
      'java',
      'java',
      'public class Main {\n  public static void main(String[] args) {\n    System.out.println("hi");\n  }\n}',
    ],
    [
      'java (private method + printf, no class keyword)',
      'java',
      'private static boolean x(String sub) {\n    System.out.printf("%s%n", sub);\n    return true;\n}',
    ],
    ['python', 'python', 'def greet(name):\n    self.name = name\n    return f"hi {name}"'],
    ['go', 'go', 'package main\n\nfunc main() {\n\tx := 1\n\tfmt.Println(x)\n}'],
    ['cpp', 'cpp', '#include <iostream>\nusing namespace std;\nint main() {\n    std::cout << "hi";\n}'],
    ['c', 'c', '#include <stdio.h>\nint main() {\n    printf("hi\\n");\n    return 0;\n}'],
    ['rust', 'rust', 'fn main() {\n    let mut x = 1;\n    std::process::exit(x);\n}'],
    ['typescript', 'typescript', 'interface Point {\n  x: number\n  y: number\n}\nconst p: Point = { x: 1, y: 2 }'],
    ['javascript', 'javascript', 'const add = (a, b) => {\n  return a + b\n}\nmodule.exports = { add }'],
    ['json', 'json', '{\n  "name": "timothy",\n  "version": "1.0.0"\n}'],
    ['yaml', 'yaml', 'name: timothy\nversion: 1.0\nsteps:\n  - build\n  - test'],
    ['bash', 'bash', '#!/bin/bash\necho "hello" && echo "world"'],
    ['sql', 'sql', 'SELECT id, name FROM users WHERE active = 1'],
    ['html', 'html', '<!doctype html>\n<html>\n<body><div>hi</div></body>\n</html>'],
    ['css', 'css', '.button {\n  color: red;\n  padding: 4px;\n}\n@media (min-width: 100px) {}'],
    ['markdown', 'markdown', '# Title\n\n- one\n- two\n\n[link](https://example.com)'],
    ['dockerfile', 'dockerfile', 'FROM node:24\nRUN npm install\nWORKDIR /app'],
    ['toml', 'toml', '[package]\nname = "timothy"\nversion = "1.0.0"'],
    ['diff', 'diff', 'diff --git a/x b/x\n@@ -1 +1 @@\n-old\n+new'],
  ]

  it.each(cases)('detects %s', (_label, expected, snippet) => {
    expect(detectLanguage(snippet)).toBe(expected)
  })

  it('stays undefined for a plain English paragraph', () => {
    const text =
      'This is just a plain paragraph of English text. It has punctuation, ' +
      'and multiple sentences; but no code-like structure at all.'
    expect(detectLanguage(text)).toBeUndefined()
  })

  it('stays undefined for a single short word', () => {
    expect(detectLanguage('plain')).toBeUndefined()
  })

  it('only scans the first 2000 characters', () => {
    const noise = 'lorem ipsum dolor sit amet '.repeat(200) // far past 2000 chars
    const text = noise + '\npackage main\nfunc main() {}\n'
    expect(detectLanguage(text)).toBeUndefined()
  })

  it('every detectable language resolves against the shiki bundled set (id or alias)', async () => {
    for (const lang of Object.keys(DETECT_PATTERNS)) {
      const resolved = await resolveShikiLangId(lang)
      expect(resolved, `${lang} does not resolve to a shiki bundled language`).toBeDefined()
    }
  })
})

describe('CodeBlock', () => {
  it('shows the language label from the fence info string', () => {
    renderMarkdown('```python\nprint(1)\n```')
    expect(screen.getByText('python')).toBeInTheDocument()
  })

  it('falls back to "text" when no language is given', () => {
    renderMarkdown('```\nplain\n```')
    expect(screen.getByText('text')).toBeInTheDocument()
  })

  it('renders one line number per source line', () => {
    renderMarkdown('```js\nline1\nline2\nline3\n```')
    expect(screen.getByText('1')).toBeInTheDocument()
    expect(screen.getByText('2')).toBeInTheDocument()
    expect(screen.getByText('3')).toBeInTheDocument()
    expect(screen.queryByText('4')).not.toBeInTheDocument()
  })

  it('does not count a trailing newline as a phantom extra line', () => {
    renderMarkdown('```js\nonly\n```')
    expect(screen.getByText('1')).toBeInTheDocument()
    expect(screen.queryByText('2')).not.toBeInTheDocument()
  })

  it('copies the raw code text via the copy button', () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('navigator', { clipboard: { writeText } })

    renderMarkdown('```js\nconst x = 1;\n```')
    fireEvent.click(screen.getByTestId('copy-button'))
    expect(writeText).toHaveBeenCalledWith('const x = 1;')
    vi.unstubAllGlobals()
  })

  it('renders the plain text immediately, before shiki resolves', () => {
    renderMarkdown('```js\nconst x = 1;\n```')
    expect(screen.queryByTestId('shiki-html')).not.toBeInTheDocument()
    expect(screen.getByText('const x = 1;')).toBeInTheDocument()
  })

  it('swaps in the shiki-highlighted markup once the highlighter resolves', async () => {
    renderMarkdown('```javascript\nconst x = 1;\n```')
    await waitFor(() => expect(screen.getByTestId('shiki-html')).toBeInTheDocument())
    expect(codeToHtml).toHaveBeenCalledWith('const x = 1;', expect.objectContaining({ lang: 'javascript' }))
  })

  it('loads and highlights a tagged php fence (the language that motivated this fix)', async () => {
    renderMarkdown('```php\n<?php echo 1; ?>\n```')
    await waitFor(() => expect(screen.getByTestId('shiki-html')).toBeInTheDocument())
    expect(loadLanguage).toHaveBeenCalled()
    expect(codeToHtml).toHaveBeenCalledWith('<?php echo 1; ?>', expect.objectContaining({ lang: 'php' }))
    expect(screen.getByText('php')).toBeInTheDocument()
  })

  it('resolves language aliases (sh/shell/yml) directly, no highlighter throw', async () => {
    renderMarkdown('```sh\necho hi\n```')
    await waitFor(() => expect(codeToHtml).toHaveBeenCalled())
    expect(codeToHtml).toHaveBeenCalledWith('echo hi', expect.objectContaining({ lang: 'sh' }))
  })

  it('falls back to plain text for a language shiki does not bundle, never calling shiki', async () => {
    renderMarkdown('```brainfuck\n+++\n```')
    await act(async () => {
      await Promise.resolve()
    })
    expect(screen.queryByTestId('shiki-html')).not.toBeInTheDocument()
    expect(screen.getByText('+++')).toBeInTheDocument()
    expect(codeToHtml).not.toHaveBeenCalledWith('+++', expect.anything())
  })

  it('detects the language from content when the fence has no tag, and labels it', () => {
    renderMarkdown(
      '```\nprivate static boolean x(String sub) {\n    System.out.printf("%s%n", sub);\n    return true;\n}\n```',
    )
    expect(screen.getByText('java')).toBeInTheDocument()
  })

  it('keeps a tagged fence verbatim even when detection would guess differently', () => {
    renderMarkdown('```text\npackage main\nfunc main() {}\n```')
    expect(screen.getByText('text')).toBeInTheDocument()
    expect(screen.queryByText('go')).not.toBeInTheDocument()
  })

  it('leaves inline code (not inside a fence) untouched, no header or gutter', () => {
    renderMarkdown('some `inline code` here')
    expect(screen.queryByTestId('copy-button')).not.toBeInTheDocument()
    expect(screen.getByText('inline code').tagName).toBe('CODE')
  })

  it('shows the vendored devicon logo in the header for a language with a mapped logo', () => {
    const { container } = renderMarkdown('```java\nclass X {}\n```')
    const header = screen.getByText('java').closest('span')
    const img = header?.querySelector('img')
    expect(img).toBeInTheDocument()
    expect(img?.getAttribute('src')).toBeTruthy()
    expect(img?.getAttribute('alt')).toBe('')
    expect(container.querySelector('.size-2.rounded-full')).not.toBeInTheDocument()
  })

  it('shows a different logo per language (php resolves via shiki bundled languages, not a curated list)', () => {
    renderMarkdown('```php\n<?php echo 1;\n```')
    const phpImg = screen.getByText('php').closest('span')?.querySelector('img')
    expect(phpImg).toBeInTheDocument()

    cleanup()
    renderMarkdown('```java\nclass X {}\n```')
    const javaImg = screen.getByText('java').closest('span')?.querySelector('img')

    expect(phpImg?.getAttribute('src')).not.toBe(javaImg?.getAttribute('src'))
  })

  it('falls back to the colored dot for a language with no mapped logo', () => {
    renderMarkdown('```toml\nkey = "value"\n```')
    const header = screen.getByText('toml').closest('span')
    expect(header?.querySelector('img')).not.toBeInTheDocument()
    expect(header?.querySelector('.size-2.rounded-full')).toBeInTheDocument()
  })

  it('wraps dark-heavy logos (bash) in a light chip so they stay visible in dark mode', () => {
    renderMarkdown('```bash\necho hi\n```')
    const header = screen.getByText('bash').closest('span')
    const chip = header?.querySelector('img')?.parentElement
    expect(chip?.className).toContain('dark:bg-white')
  })

  it('does not add the dark-mode chip to a logo that already reads fine on dark', () => {
    renderMarkdown('```java\nclass X {}\n```')
    const header = screen.getByText('java').closest('span')
    const chip = header?.querySelector('img')?.parentElement
    expect(chip?.className).not.toContain('dark:bg-white')
  })
})
