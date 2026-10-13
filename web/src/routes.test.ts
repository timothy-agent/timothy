import { describe, expect, it } from 'vitest'
import appSource from './App.tsx?raw'
import { appRoutes } from './routes'

describe('appRoutes', () => {
  it('lists exactly the <Route path> values in App.tsx', () => {
    const inApp = [...appSource.matchAll(/path="([^"]+)"/g)].map((m) => m[1]).sort()
    // /welcome, /login, /setup are matched on pathname in App.tsx, not declared as a <Route>.
    const outside = new Set(['/welcome', '/login', '/setup'])
    const listed = appRoutes.map((r) => r.path).filter((p) => !outside.has(p)).sort()
    expect(inApp.length).toBeGreaterThan(0)
    expect(listed).toEqual(inApp)
  })

  it('has no duplicate paths', () => {
    const paths = appRoutes.map((r) => r.path)
    expect(new Set(paths).size).toBe(paths.length)
  })
})
