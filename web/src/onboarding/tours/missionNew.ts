import type { TourDef } from '../tour/types'

export const missionNewTour: TourDef = {
  page: 'mission_new',
  version: 1,
  steps: [
    {
      target: 'missions.form.goal',
      title: 'Say what done looks like',
      body: 'One clear goal with the result you want. Timothy plans the steps.',
    },
    {
      target: 'missions.form.flow',
      title: 'How thorough',
      body: 'Full plans, builds and verifies. Light is a single pass for quick deliverables.',
    },
    {
      target: 'missions.form.create',
      title: 'Run it',
      body: 'You can watch each phase and step in while it runs.',
      side: 'top',
    },
  ],
}
