// DestinationPreset is one entry of the "Add a destination" tile grid,
// mirroring connectorPresets.ts's shape for the fixed set of
// destination kinds.
export interface DestinationPreset {
  id: 'email' | 'webhook' | 'channel' | 'github' | 'bitbucket' | 'gitlab'
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
    id: 'channel',
    name: 'Channel',
    description: 'Sends through a Telegram, Slack or email channel',
  },
  {
    id: 'github',
    name: 'GitHub',
    description: 'Pushes a branch or opens a PR via a connector',
  },
  {
    id: 'bitbucket',
    name: 'Bitbucket',
    description: 'Pushes a branch or opens a PR via a connector',
  },
  {
    id: 'gitlab',
    name: 'GitLab',
    description: 'Pushes a branch or opens a merge request via a connector',
  },
]
