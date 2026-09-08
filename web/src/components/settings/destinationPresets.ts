// DestinationPreset is one entry of the "Add a destination" tile grid,
// mirroring connectorPresets.ts's shape for the fixed set of
// destination kinds.
export interface DestinationPreset {
  id: 'email' | 'webhook' | 'telegram' | 'github'
  name: string
  description: string
}

export const destinationPresets: DestinationPreset[] = [
  {
    id: 'email',
    name: 'Email',
    description: 'Sends via a connected Gmail account',
  },
  {
    id: 'webhook',
    name: 'Webhook',
    description: 'POSTs the digest as JSON or plain text',
  },
  {
    id: 'telegram',
    name: 'Telegram',
    description: 'Sends via a bot to a chat',
  },
  {
    id: 'github',
    name: 'GitHub',
    description: 'Pushes a branch or opens a PR via a connector',
  },
]
