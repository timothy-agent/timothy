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

  it('classifies known code extensions', () => {
    expect(previewKindOf('main.go')).toBe('code')
    expect(previewKindOf('index.tsx')).toBe('code')
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
  })

  it('returns undefined for unmapped extensions', () => {
    expect(codeLanguageOf('archive.zip')).toBeUndefined()
  })
})
