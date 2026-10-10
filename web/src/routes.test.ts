import { describe, expect, it } from 'vitest'
import appSource from './App.tsx?raw'
import { appRoutes } from './routes'

describe('appRoutes', () => {
  it('lists exactly the <Route path> values in App.tsx', () => {
    const inApp = [...appSource.matchAll(/path="([^"]+)"/g)].map((m) => m[1]).sort()
    // /welcome is matched on pathname in App.tsx, not declared as a <Route>.
    const listed = appRoutes.map((r) => r.path).filter((p) => p !== '/welcome').sort()
    expect(inApp.length).toBeGreaterThan(0)
    expect(listed).toEqual(inApp)
  })

  it('has no duplicate paths', () => {
    const paths = appRoutes.map((r) => r.path)
    expect(new Set(paths).size).toBe(paths.length)
  })
})
