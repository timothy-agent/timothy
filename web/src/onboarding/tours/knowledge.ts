import type { TourDef } from '../tour/types'

export const knowledgeTour: TourDef = {
  page: 'knowledge',
  version: 1,
  steps: [
    {
      target: 'knowledge.new',
      title: 'Collections',
      body: 'A collection is a folder of documents Timothy can search and quote.',
      side: 'left',
    },
    {
      target: 'knowledge.list',
      title: 'Use it in chat',
      body: 'Type # in the chat box to pin a collection, or add it to an agent so it is always on.',
      side: 'top',
    },
  ],
}
