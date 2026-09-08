import { describe, expect, it } from 'vitest'
import { codeLanguageOf, previewKindOf } from './filePreviewKind'

describe('previewKindOf', () => {
  it('classifies images', () => {
    expect(previewKindOf('logo.png')).toBe('image')
    expect(previewKindOf('a/b/photo.JPG')).toBe('image')
  })

  it('classifies markdown', () => {
    expect(previewKindOf('README.md')).toBe('markdown')
  })

  it('classifies pdf', () => {
    expect(previewKindOf('report.pdf')).toBe('pdf')
    expect(previewKindOf('a/b/report.PDF')).toBe('pdf')
  })

  it('classifies known code extensions', () => {
    expect(previewKindOf('main.go')).toBe('code')
    expect(previewKindOf('index.tsx')).toBe('code')
  })

  it('classifies xml and php', () => {
    expect(previewKindOf('config.xml')).toBe('code')
    expect(previewKindOf('index.php')).toBe('code')
  })

  it('classifies known dotfiles and extensionless files by basename', () => {
    expect(previewKindOf('.gitignore')).toBe('code')
    expect(previewKindOf('a/b/.gitignore')).toBe('code')
    expect(previewKindOf('Makefile')).toBe('code')
    expect(previewKindOf('Dockerfile')).toBe('code')
    expect(previewKindOf('go.mod')).toBe('code')
  })

  it('treats only known binary formats as unsupported', () => {
    expect(previewKindOf('archive.zip')).toBe('unsupported')
    expect(previewKindOf('fonts/a.woff2')).toBe('unsupported')
    expect(previewKindOf('clip.mp4')).toBe('unsupported')
  })

  it('previews unknown extensions and extensionless files as text', () => {
    expect(previewKindOf('noext')).toBe('code')
    expect(previewKindOf('.gitkeep')).toBe('code')
    expect(previewKindOf('data.csv')).toBe('code')
    expect(previewKindOf('notes.org')).toBe('code')
    expect(codeLanguageOf('notes.org')).toBeUndefined()
  })
})

describe('codeLanguageOf', () => {
  it('maps known extensions to a shiki language id', () => {
    expect(codeLanguageOf('main.go')).toBe('go')
    expect(codeLanguageOf('index.tsx')).toBe('tsx')
    expect(codeLanguageOf('app.jsx')).toBe('jsx')
    expect(codeLanguageOf('page.html')).toBe('html')
    expect(codeLanguageOf('Cargo.toml')).toBe('toml')
    expect(codeLanguageOf('config.xml')).toBe('xml')
    expect(codeLanguageOf('index.php')).toBe('php')
  })

  it('maps known basenames to a shiki language id', () => {
    expect(codeLanguageOf('.gitignore')).toBe('plaintext')
    expect(codeLanguageOf('Makefile')).toBe('makefile')
    expect(codeLanguageOf('Dockerfile')).toBe('dockerfile')
    expect(codeLanguageOf('go.mod')).toBe('plaintext')
  })

  it('returns undefined for unmapped extensions', () => {
    expect(codeLanguageOf('archive.zip')).toBeUndefined()
  })
})
