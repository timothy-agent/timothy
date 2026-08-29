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

  it('falls back to unsupported for unknown extensions', () => {
    expect(previewKindOf('archive.zip')).toBe('unsupported')
    expect(previewKindOf('noext')).toBe('unsupported')
  })
})

describe('codeLanguageOf', () => {
  it('maps known extensions to a highlight.js language', () => {
    expect(codeLanguageOf('main.go')).toBe('go')
    expect(codeLanguageOf('index.tsx')).toBe('typescript')
    expect(codeLanguageOf('config.xml')).toBe('xml')
    expect(codeLanguageOf('index.php')).toBe('php')
  })

  it('maps known basenames to a highlight.js language', () => {
    expect(codeLanguageOf('.gitignore')).toBe('plaintext')
    expect(codeLanguageOf('Makefile')).toBe('makefile')
    expect(codeLanguageOf('Dockerfile')).toBe('dockerfile')
    expect(codeLanguageOf('go.mod')).toBe('plaintext')
  })

  it('returns undefined for unmapped extensions', () => {
    expect(codeLanguageOf('archive.zip')).toBeUndefined()
  })
})
