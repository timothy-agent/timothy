import type { Readiness, ReadinessKey } from '../api/types'

export interface GateCopy {
  title: string
  description: string
  action?: { label: string; to: string }
}

const addProvider = { label: 'Add a provider', to: '/settings/providers' }

// gateCopy is what a SetupGate shows for each unmet key. Plain words:
// the operator never sees the term "route" here.
export const gateCopy: Partial<Record<ReadinessKey, GateCopy>> = {
  gateway_ready: {
    title: "Timothy can't reach its model gateway",
    description: 'The gateway service is not answering. Check that every service in the stack is running.',
  },
  chat_route: {
    title: 'Timothy has no model for chat yet',
    description: 'Add a provider so Timothy can answer.',
    action: addProvider,
  },
  embedding_route: {
    title: 'Knowledge needs an embedding model',
    description: 'Add a provider with an embedding model so documents can be searched.',
    action: addProvider,
  },
  vision_route: {
    title: 'No model can look at images yet',
    description: 'Add a provider with a vision model to send images.',
    action: addProvider,
  },
  sandbox: {
    title: 'Missions need the sandbox service',
    description: 'The sandbox service is not reachable, so missions cannot run. Check the stack logs and restart it.',
  },
  automations_enabled: {
    title: 'Automations are switched off',
    description: 'Turn them on in Settings to run work on a schedule or trigger.',
    action: { label: 'Open Features', to: '/settings/features' },
  },
}

const modelKeys: ReadinessKey[] = ['chat_route', 'summarize_route', 'embedding_route', 'vision_route']

// unmetKey returns the key a gate should explain, or null when every
// required key is met. A down gateway explains any unmet model key.
export function unmetKey(readiness: Readiness | null, requires: ReadinessKey[]): ReadinessKey | null {
  if (!readiness) return null
  const missing = requires.find((k) => !readiness[k])
  if (!missing) return null
  if (!readiness.gateway_ready && requires.some((k) => modelKeys.includes(k))) return 'gateway_ready'
  return missing
}
