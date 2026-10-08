import type { TourDef } from '../tour/types'

export const chatTour: TourDef = {
  page: 'chat',
  version: 1,
  steps: [
    {
      target: 'chat.composer',
      title: 'Ask anything',
      body: 'Type here and press Enter. Type # to pull in a knowledge collection or @ to reference a mission.',
      side: 'top',
    },
    {
      target: 'chat.picker',
      title: 'Who answers',
      body: 'Pick the agent and the model that answer this chat. The default agent is a good start.',
      side: 'top',
    },
    {
      target: 'chat.attach',
      title: 'Attach files',
      body: 'Drop in PDFs, images or audio. Timothy reads them before answering.',
      side: 'top',
    },
    {
      target: 'chat.sessions',
      title: 'Your chats',
      body: 'Every chat is kept here. Search by title or by what you said.',
      side: 'right',
    },
    {
      target: 'chat.permissions',
      title: 'Permission requests',
      body: 'When Timothy needs your OK to run a tool, a badge shows here.',
      side: 'right',
    },
  ],
}
