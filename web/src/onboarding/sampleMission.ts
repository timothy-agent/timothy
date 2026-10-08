import type { CreateMissionInput } from '../api/client'

const pad = (n: number) => String(n).padStart(2, '0')

// sampleMissionInput is the canned first mission: no connector, no
// repo, and a run tag so every run is unique.
export function sampleMissionInput(now = new Date()): CreateMissionInput {
  const tag =
    `${now.getFullYear()}${pad(now.getMonth() + 1)}${pad(now.getDate())}` +
    `-${pad(now.getHours())}${pad(now.getMinutes())}${pad(now.getSeconds())}`
  return {
    goal:
      'Write a short guide, under 200 words, on how to get the most out of Timothy missions: ' +
      'when to use a mission instead of a chat, how to phrase a good goal, and what to expect in the result. ' +
      `Run tag: onboarding-${tag}.`,
    kind: 'general',
    light: true,
  }
}
