import type { Readiness } from '../api/types'
import type { OnboardingState } from './context'

// readyReadiness is a fully set-up instance, for tests that render
// gated pages without exercising the gates.
export const readyReadiness: Readiness = {
  gateway_ready: true,
  chat_route: true,
  summarize_route: true,
  embedding_route: true,
  vision_route: true,
  sandbox: true,
  first_chat: true,
  first_mission: true,
  connectors: 0,
  channels: 0,
  kb_collections: 0,
  automations: 0,
  automations_enabled: true,
}

// onboardingState builds a static context value for tests.
export function onboardingState(readiness: Readiness | null = readyReadiness): OnboardingState {
  return {
    readiness,
    progress: {},
    loading: false,
    error: null,
    refresh: async () => {},
    updateProgress: async () => {},
  }
}
