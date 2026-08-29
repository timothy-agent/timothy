export type PreviewKind = 'image' | 'markdown' | 'pdf' | 'code' | 'unsupported'

const imageExts = new Set(['png', 'jpg', 'jpeg', 'gif', 'svg', 'webp', 'bmp', 'ico'])
const markdownExts = new Set(['md', 'markdown'])

// codeLanguage maps an extension to a highlight.js language name for
// files that aren't markdown/images — undefined lets highlight.js
// auto-detect, exts here are just the common cases worth naming
// explicitly (e.g. .go isn't unambiguous from content alone).
const codeLanguages: Record<string, string> = {
  go: 'go',
  ts: 'typescript',
  tsx: 'typescript',
  js: 'javascript',
  jsx: 'javascript',
  py: 'python',
  rb: 'ruby',
  rs: 'rust',
  java: 'java',
  c: 'c',
  h: 'c',
  cpp: 'cpp',
  cc: 'cpp',
  sh: 'bash',
  bash: 'bash',
  yml: 'yaml',
  yaml: 'yaml',
  json: 'json',
  toml: 'ini',
  sql: 'sql',
  html: 'xml',
  xml: 'xml',
  php: 'php',
  kt: 'kotlin',
  swift: 'swift',
  cs: 'csharp',
  gradle: 'groovy',
  ini: 'ini',
  cfg: 'ini',
  conf: 'ini',
  properties: 'ini',
  dockerfile: 'dockerfile',
  makefile: 'makefile',
  mod: 'plaintext',
  sum: 'plaintext',
  lock: 'plaintext',
  css: 'css',
  txt: 'plaintext',
}

// basenameLanguages maps extensionless/dotfile basenames (exact match,
// case-sensitive per convention) to a highlight.js language.
const basenameLanguages: Record<string, string> = {
  '.gitignore': 'plaintext',
  '.gitattributes': 'plaintext',
  '.gitmodules': 'ini',
  Makefile: 'makefile',
  Dockerfile: 'dockerfile',
  LICENSE: 'plaintext',
  NOTICE: 'plaintext',
  'go.mod': 'plaintext',
  'go.sum': 'plaintext',
}

function baseOf(path: string): string {
  return path.split('/').pop() ?? path
}

function extOf(path: string): string {
  const base = baseOf(path)
  const dot = base.lastIndexOf('.')
  return dot === -1 ? '' : base.slice(dot + 1).toLowerCase()
}

export function previewKindOf(path: string): PreviewKind {
  const ext = extOf(path)
  if (imageExts.has(ext)) return 'image'
  if (markdownExts.has(ext)) return 'markdown'
  if (ext === 'pdf') return 'pdf'
  if (baseOf(path) in basenameLanguages) return 'code'
  if (ext in codeLanguages) return 'code'
  return 'unsupported'
}

export function codeLanguageOf(path: string): string | undefined {
  return basenameLanguages[baseOf(path)] ?? codeLanguages[extOf(path)]
}
