import { describe, expect, it } from 'vitest'
import { settingsAreas } from './components/settings/settingsAreas'
import { appRoutes } from './routes'
import { routesExport } from './routesExport'

describe('routesExport', () => {
  const out = routesExport()

  it('exports every route sorted by path', () => {
    const paths = out.routes.map((r) => r.path)
    expect(paths).toEqual([...paths].sort())
    expect(paths.length).toBe(appRoutes.length)
    expect(out.routes).toContainEqual({ path: '/missions', label: 'Missions' })
    expect(out.routes).toContainEqual({ path: '/design' })
  })

  it('exports settings areas with their routes', () => {
    expect(out.settings_areas.length).toBe(settingsAreas.length)
    expect(out.settings_areas).toContainEqual({ key: 'features', label: 'Features', path: '/settings/features' })
    for (const a of out.settings_areas) expect(a.path).toBe(`/settings/${a.key}`)
  })

  it('exports sorted unique labels from routes and settings areas', () => {
    expect(out.labels).toEqual([...new Set(out.labels)].sort())
    expect(out.labels).toContain('Missions')
    expect(out.labels).toContain('Features')
    expect(out.labels).not.toContain(undefined)
  })

  it('is deterministic', () => {
    expect(JSON.stringify(routesExport())).toBe(JSON.stringify(out))
  })
})
