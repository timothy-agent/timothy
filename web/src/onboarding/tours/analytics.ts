import type { TourDef } from '../tour/types'

export const analyticsTour: TourDef = {
  page: 'analytics',
  version: 1,
  steps: [
    {
      target: 'analytics.range',
      title: 'Costs over time',
      body: "Every chat and mission is priced from the provider's list price. Pick a range.",
      side: 'left',
    },
    {
      target: 'analytics.budget',
      title: 'Set a budget',
      body: 'A monthly or daily cap stops spend before it surprises you.',
    },
    {
      target: 'analytics.providers',
      title: 'Where it goes',
      body: 'Compare providers and models to see what each one costs you.',
      side: 'top',
    },
  ],
}
