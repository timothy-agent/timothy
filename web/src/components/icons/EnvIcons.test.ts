import { describe, expect, it } from 'vitest'
import { envIcon } from './EnvIcons'

describe('envIcon', () => {
  it('returns an icon component for each known language environment', () => {
    for (const env of ['go', 'node', 'python', 'java', 'php']) {
      expect(envIcon(env)).not.toBeNull()
    }
  })

  it('returns null for base, empty, and unknown environments', () => {
    expect(envIcon('base')).toBeNull()
    expect(envIcon('')).toBeNull()
    expect(envIcon('junk')).toBeNull()
  })
})
