import { describe, expect, it } from 'vitest'
import type { MissionFile } from '../../api/types'
import { buildFileTree } from './fileTree'

function file(path: string, size = 1): MissionFile {
  return { path, size, mtime: '2026-01-01T00:00:00Z', declared: false }
}

describe('buildFileTree', () => {
  it('nests files under their directories', () => {
    const tree = buildFileTree([file('src/a.txt'), file('src/sub/b.txt'), file('README.md')])

    const readme = tree.find((n) => n.name === 'README.md')
    const src = tree.find((n) => n.name === 'src')
    expect(readme?.file?.path).toBe('README.md')
    expect(src?.children.map((c) => c.name)).toEqual(['sub', 'a.txt'])
    const sub = src?.children.find((c) => c.name === 'sub')
    expect(sub?.children[0].file?.path).toBe('src/sub/b.txt')
  })

  it('sorts folders before files, alphabetically within each group', () => {
    const tree = buildFileTree([file('z.txt'), file('a.txt'), file('m/x.txt'), file('b/y.txt')])
    expect(tree.map((n) => n.name)).toEqual(['b', 'm', 'a.txt', 'z.txt'])
  })

  it('merges two files under the same directory into one folder node', () => {
    const tree = buildFileTree([file('dir/a.txt'), file('dir/b.txt')])
    expect(tree).toHaveLength(1)
    expect(tree[0].children).toHaveLength(2)
  })
})
