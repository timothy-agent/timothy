import type { TourDef } from '../tour/types'

export const automationsTour: TourDef = {
  page: 'automations',
  version: 1,
  steps: [
    {
      target: 'automations.new',
      title: 'Automate work',
      body: 'An automation runs a mission on a schedule or when something happens, like a GitHub event or a channel message.',
      side: 'left',
    },
    {
      target: 'automations.templates',
      title: 'Start from a template',
      body: 'Pick a ready-made automation and adjust it.',
      side: 'top',
    },
    {
      target: 'automations.list',
      title: 'Your automations',
      body: 'See the last run, pause one, or open its history.',
      side: 'top',
    },
  ],
}
