import { describe, expect, it } from 'vitest'
import { slugify } from './slugify'

describe('slugify', () => {
  it('lowercases and hyphenates non-alphanumeric runs', () => {
    expect(slugify('My Homelab Agent!')).toBe('my-homelab-agent')
  })

  it('trims leading and trailing hyphens', () => {
    expect(slugify('  --Infra--  ')).toBe('infra')
  })
})
