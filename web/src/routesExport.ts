import { settingsAreas } from './components/settings/settingsAreas.ts'
import { appRoutes } from './routes.ts'

export type RoutesExport = {
  routes: { path: string; label?: string }[]
  settings_areas: { key: string; label: string; path: string }[]
  labels: string[]
}

const byCodeUnit = (a: string, b: string) => (a < b ? -1 : a > b ? 1 : 0)

// The shape of web/routes.generated.json, which cmd/manifest and the
// docs validator read. Sorted so the file only changes when routes do.
export function routesExport(): RoutesExport {
  const routes = appRoutes
    .map((r) => (r.label ? { path: r.path, label: r.label } : { path: r.path }))
    .sort((a, b) => byCodeUnit(a.path, b.path))
  const areas = settingsAreas
    .map((a) => ({ key: a.key as string, label: a.label as string, path: `/settings/${a.key}` }))
    .sort((a, b) => byCodeUnit(a.key, b.key))
  const labels = [
    ...new Set([...routes.flatMap((r) => (r.label ? [r.label] : [])), ...areas.map((a) => a.label)]),
  ].sort(byCodeUnit)
  return { routes, settings_areas: areas, labels }
}
