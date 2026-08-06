import { useEffect, useState } from 'react'
import { listAgents, listRoutes } from '../api/client'
import type { AdminAgent, AdminRoute } from '../api/types'

// Sentinel value meaning "let the system pick" (D-034 follow-up) —
// matches the backend's autoAgentName. Not a real agent row: the
// picker synthesizes its entry, chat.Service resolves it per turn via
// agents.Dispatch before the normal agent lookup.
export const AUTO_AGENT = 'auto'

// Module-level cache: the picker renders in every composer; the agent
// list changes rarely and a stale list self-heals on the next mount.
let cachedAgents: AdminAgent[] | null = null

export function useAgents(): AdminAgent[] {
  const [agents, setAgents] = useState<AdminAgent[]>(cachedAgents ?? [])
  useEffect(() => {
    listAgents().then(
      (list) => {
        cachedAgents = list.filter((a) => a.enabled)
        setAgents(cachedAgents)
      },
      () => undefined,
    )
  }, [])
  return agents
}

// Module-level cache: the picker renders in every composer; the route
// list changes rarely and a stale list self-heals on the next mount.
let cachedRoutes: AdminRoute[] | null = null

export function useRoutes(): AdminRoute[] | null {
  const [routes, setRoutes] = useState<AdminRoute[] | null>(cachedRoutes)
  useEffect(() => {
    listRoutes().then(
      (list) => {
        cachedRoutes = list
        setRoutes(cachedRoutes)
      },
      () => undefined, // admin proxy may be unreachable — picker hides itself
    )
  }, [])
  return routes
}
