import { useEffect, useState } from 'react'
import { availableModels, listAgents, listProviders, listRoutes } from '../api/client'
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

// ProviderModelOption is one pin value for the chat/mission model
// pickers: "ProviderName/model" matches gateway model_hint / D-078 pins.
export type ProviderModelOption = {
  provider_name: string
  model: string
  display_name?: string
  value: string
}

// Module-level cache: listing every provider's models is chatty; the
// picker remounts often and a stale list self-heals on the next full
// page load. null means "not loaded or failed" — callers hide the
// Model section rather than showing a broken picker.
let cachedProviderModels: ProviderModelOption[] | null = null
let providerModelsFailed = false

// resetProviderModelsCache clears the module cache so a test can drive
// a fresh listProviders/availableModels round without resetModules.
export function resetProviderModelsCache() {
  cachedProviderModels = null
  providerModelsFailed = false
}

export function useProviderModels(): ProviderModelOption[] | null {
  const [models, setModels] = useState<ProviderModelOption[] | null>(
    providerModelsFailed ? null : cachedProviderModels,
  )
  useEffect(() => {
    if (cachedProviderModels !== null || providerModelsFailed) return
    let cancelled = false
    listProviders()
      .then(async (providers) => {
        // kind='cli' rows are mission-only harness executors (D-051);
        // the chat router never serves them, so they are not pickable.
        const enabled = providers.filter((p) => p.enabled && p.kind !== 'cli')
        const perProvider = await Promise.all(
          enabled.map(async (p) => {
            try {
              // The provider's one model list (D-172): a cliproxy row
              // lists only its own vendor, and non-chat ids are gone.
              const listed = await availableModels(p.id)
              if (listed.length === 0 && p.default_model) {
                return [
                  {
                    provider_name: p.name,
                    model: p.default_model,
                    value: `${p.name}/${p.default_model}`,
                  },
                ]
              }
              return listed.map((m) => ({
                provider_name: p.name,
                model: m.id,
                display_name: m.display_name,
                value: `${p.name}/${m.id}`,
              }))
            } catch {
              // A failed listing (a rejected key, say) falls back to
              // the provider's configured default_model when present.
              if (!p.default_model) return []
              return [
                {
                  provider_name: p.name,
                  model: p.default_model,
                  value: `${p.name}/${p.default_model}`,
                },
              ]
            }
          }),
        )
        const flat = perProvider.flat()
        cachedProviderModels = flat
        if (!cancelled) setModels(flat)
      })
      .catch(() => {
        providerModelsFailed = true
        if (!cancelled) setModels(null)
      })
    return () => {
      cancelled = true
    }
  }, [])
  return models
}
