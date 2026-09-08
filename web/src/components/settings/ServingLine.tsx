import type { AdminRoute } from '../../api/types'

// ServingLine renders a route's current serving state, shared by
// RoutesList and RouteEdit (contract 14.14): the router's own verdict
// (first usable chain entry), or why nothing is serving yet. Exact
// strings preserved: "serving", "no usable provider", "stats loading…".
export function ServingLine({
  route,
  nameOf,
}: {
  route: Pick<AdminRoute, 'enabled' | 'serving' | 'resolved'>
  nameOf: (id: string) => string
}) {
  if (route.serving) {
    return (
      <p className="text-xs text-muted-foreground">
        serving <span className="text-foreground">{nameOf(route.serving.provider_id)}</span> /{' '}
        <span className="font-mono text-foreground">{route.serving.model}</span>
      </p>
    )
  }
  if (!route.enabled) {
    return <p className="text-xs font-medium text-warning">disabled</p>
  }
  if (route.resolved) {
    return <p className="text-xs font-medium text-warning">no usable provider</p>
  }
  return <p className="text-xs text-muted-foreground">stats loading…</p>
}
