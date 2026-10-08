import type { TourDef } from '../tour/types'

export const missionsTour: TourDef = {
  page: 'missions',
  version: 1,
  steps: [
    {
      target: 'missions.new',
      title: 'Start a mission',
      body: 'A mission is a longer task Timothy works on by itself and reports back.',
      side: 'left',
    },
    {
      target: 'missions.filters',
      title: 'Filter',
      body: 'Narrow the list by kind, harness, model or source.',
    },
  ],
}
