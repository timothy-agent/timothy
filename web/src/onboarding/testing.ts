import type { Readiness } from '../api/types'
import type { OnboardingState } from './context'
import { chatTour } from './tours/chat'
import { missionNewTour } from './tours/missionNew'
import { missionsTour } from './tours/missions'

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

// Every tour marked seen, so page tests render without a tour overlay.
const toursSeen = Object.fromEntries([chatTour, missionsTour, missionNewTour].map((t) => [t.page, t.version]))

// onboardingState builds a static context value for tests.
export function onboardingState(readiness: Readiness | null = readyReadiness): OnboardingState {
  return {
    readiness,
    progress: { tours_seen: toursSeen },
    loading: false,
    error: null,
    refresh: async () => {},
    updateProgress: async () => {},
  }
}
