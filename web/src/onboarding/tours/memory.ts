import type { TourDef } from '../tour/types'

export const memoryTour: TourDef = {
  page: 'memory',
  version: 1,
  steps: [
    {
      target: 'memory.view',
      title: 'What Timothy remembers',
      body: 'Facts it picked up from chats and missions. Switch to the graph to see how they connect.',
      side: 'left',
    },
    {
      target: 'memory.status',
      title: 'Review pending memories',
      body: 'New memories wait here for your OK. Reject anything wrong and Timothy forgets it. A newer fact replaces an older one.',
    },
  ],
}
