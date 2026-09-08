export type PreviewKind = 'image' | 'markdown' | 'pdf' | 'code' | 'unsupported'

const imageExts = new Set(['png', 'jpg', 'jpeg', 'gif', 'svg', 'webp', 'bmp', 'ico'])
// Known binary formats that no text preview can show. Anything not
// listed here or above previews as text; FileViewer still bails out on
// content that turns out to be binary.
const binaryExts = new Set([
  'zip', 'tar', 'gz', 'tgz', 'bz2', 'xz', 'zst', '7z', 'rar', 'jar', 'war',
  'exe', 'dll', 'so', 'dylib', 'bin', 'o', 'a', 'wasm', 'class', 'pyc',
  'woff', 'woff2', 'ttf', 'otf', 'eot',
  'mp3', 'wav', 'ogg', 'flac', 'm4a', 'mp4', 'mov', 'avi', 'mkv', 'webm',
  'sqlite', 'db', 'parquet', 'avro', 'pb', 'psd', 'ai', 'heic', 'tiff', 'tif',
])
const markdownExts = new Set(['md', 'markdown'])

// codeLanguage maps an extension to a shiki language id for files
// that aren't markdown/images. undefined falls back to CodeBlock's
// content heuristic; exts here are just the common cases worth naming
// explicitly (e.g. .go isn't unambiguous from content alone).
const codeLanguages: Record<string, string> = {
  go: 'go',
  ts: 'typescript',
  tsx: 'tsx',
  js: 'javascript',
  jsx: 'jsx',
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
  toml: 'toml',
  sql: 'sql',
  html: 'html',
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
// case-sensitive per convention) to a shiki language id.
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
  if (binaryExts.has(ext)) return 'unsupported'
  return 'code'
}

export function codeLanguageOf(path: string): string | undefined {
  return basenameLanguages[baseOf(path)] ?? codeLanguages[extOf(path)]
}
