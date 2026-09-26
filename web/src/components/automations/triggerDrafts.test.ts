import { describe, expect, it } from 'vitest'
import type { AdminConnector } from '../../api/types'
import {
  draftsFromTriggers,
  draftsToInput,
  filterPathError,
  githubConnectorOptions,
  goalHint,
  newTriggerDraft,
  patternError,
  pendingSecrets,
  repoError,
  secretFieldError,
  triggerError,
} from './triggerDrafts'

const connectorID = '0000000c-0000-0000-0000-000000000001'

const connector = (over: Partial<AdminConnector>): AdminConnector => ({
  id: connectorID,
  name: 'gh',
  kind: 'github',
  config: {},
  credential_ref: 'GH_PAT',
  enabled: true,
  sensitive: false,
  ...over,
})

describe('connector_event drafts', () => {
  const stored = {
    id: 't5',
    kind: 'connector_event' as const,
    config: { connector_id: connectorID, repo: 'octo/timothy', events: ['pr.opened', 'pr.labeled'], labels: ['needs-review'] },
    enabled: true,
    tool_allowlist: ['github_read_pr'],
  }

  it('loads the config and keeps the id', () => {
    const [d] = draftsFromTriggers([stored])
    expect(d).toMatchObject({
      id: 't5',
      kind: 'connector_event',
      connectorId: connectorID,
      repo: 'octo/timothy',
      events: ['pr.opened', 'pr.labeled'],
      labels: ['needs-review'],
      toolAllowlist: ['github_read_pr'],
    })
  })

  it('round-trips to the wire shape without a credential_ref', () => {
    expect(draftsToInput(draftsFromTriggers([stored]))).toEqual([
      {
        id: 't5',
        kind: 'connector_event',
        config: { connector_id: connectorID, repo: 'octo/timothy', events: ['pr.opened', 'pr.labeled'], labels: ['needs-review'] },
        enabled: true,
        tool_allowlist: ['github_read_pr'],
      },
    ])
  })

  it('omits empty labels and an empty allowlist, trims the repo', () => {
    const d = { ...newTriggerDraft(), kind: 'connector_event' as const, connectorId: connectorID, repo: ' octo/timothy ', events: ['pr.opened'], toolAllowlist: [] }
    expect(draftsToInput([d])).toEqual([
      { kind: 'connector_event', config: { connector_id: connectorID, repo: 'octo/timothy', events: ['pr.opened'] }, enabled: true },
    ])
  })
})

describe('webhook drafts', () => {
  const stored = {
    id: 't6',
    kind: 'webhook' as const,
    config: { scheme: 'generic' as const, filters: [{ path: '$.action', equals: 'opened' }] },
    credential_ref: 'HOOK_KEY',
    enabled: false,
  }

  it('loads scheme, filters without the $. prefix, and the secret name', () => {
    const [d] = draftsFromTriggers([stored])
    expect(d).toMatchObject({
      id: 't6',
      scheme: 'generic',
      filters: [{ path: 'action', equals: 'opened' }],
      credentialRef: 'HOOK_KEY',
      secretMode: 'existing',
      secretValue: '',
      enabled: false,
    })
  })

  it('a fresh draft starts in new mode with no secret value', () => {
    const d = newTriggerDraft()
    expect(d.secretMode).toBe('new')
    expect(d.secretValue).toBe('')
  })

  it('round-trips with credential_ref at the top level and never a secretValue', () => {
    const [d] = draftsFromTriggers([stored])
    expect(draftsToInput([{ ...d, secretValue: 'should-not-leak' }])).toEqual([
      { id: 't6', kind: 'webhook', config: { scheme: 'generic', filters: [{ path: 'action', equals: 'opened' }] }, credential_ref: 'HOOK_KEY', enabled: false },
    ])
  })

  it('omits empty filters', () => {
    const d = { ...newTriggerDraft(), kind: 'webhook' as const, credentialRef: ' HOOK_KEY ' }
    expect(draftsToInput([d])).toEqual([{ kind: 'webhook', config: { scheme: 'github' }, credential_ref: 'HOOK_KEY', enabled: true }])
  })
})

describe('secretFieldError', () => {
  it('new mode: needs a name, a valid shape, then a value', () => {
    const hook = { ...newTriggerDraft(), kind: 'webhook' as const }
    expect(secretFieldError(hook)).toBe('Enter the signing secret name.')
    expect(secretFieldError({ ...hook, credentialRef: 'bad name!' })).toBe('Use letters, digits, _ . / or -.')
    expect(secretFieldError({ ...hook, credentialRef: 'HOOK_KEY' })).toBe('Paste the signing key.')
    expect(secretFieldError({ ...hook, credentialRef: 'HOOK_KEY', secretValue: 'k3y' })).toBeUndefined()
  })

  it('existing mode: needs a picked name only', () => {
    const hook = { ...newTriggerDraft(), kind: 'webhook' as const, secretMode: 'existing' as const }
    expect(secretFieldError(hook)).toBe('Pick a stored secret.')
    expect(secretFieldError({ ...hook, credentialRef: 'HOOK_KEY' })).toBeUndefined()
  })
})

describe('pendingSecrets', () => {
  it('collects new-mode webhook secrets with a value, deduped by name', () => {
    const a = { ...newTriggerDraft(), kind: 'webhook' as const, credentialRef: 'HOOK_KEY', secretValue: 'k3y' }
    const b = { ...newTriggerDraft(), kind: 'webhook' as const, credentialRef: 'HOOK_KEY', secretValue: 'k3y2' }
    const existing = { ...newTriggerDraft(), kind: 'webhook' as const, secretMode: 'existing' as const, credentialRef: 'OTHER' }
    const empty = { ...newTriggerDraft(), kind: 'webhook' as const, credentialRef: 'EMPTY' }
    expect(pendingSecrets([a, b, existing, empty])).toEqual([{ name: 'HOOK_KEY', value: 'k3y2' }])
  })

  it('trims pasted whitespace from the key', () => {
    const d = { ...newTriggerDraft(), kind: 'webhook' as const, credentialRef: 'HOOK_KEY', secretValue: '  k3y\n' }
    expect(pendingSecrets([d])).toEqual([{ name: 'HOOK_KEY', value: 'k3y' }])
  })

  it('ignores non-webhook and blank-name drafts', () => {
    expect(pendingSecrets([newTriggerDraft(), { ...newTriggerDraft(), kind: 'connector_event' as const }])).toEqual([])
  })
})

describe('triggerError', () => {
  const base = { ...newTriggerDraft(), kind: 'connector_event' as const }

  it('walks the connector event fields in order', () => {
    expect(triggerError(base)).toBe('Pick a GitHub connector.')
    expect(triggerError({ ...base, connectorId: connectorID })).toBe('Enter a repository.')
    expect(triggerError({ ...base, connectorId: connectorID, repo: 'timothy' })).toMatch(/owner\/name/)
    expect(triggerError({ ...base, connectorId: connectorID, repo: 'octo/timothy' })).toBe('Pick at least one event.')
    expect(triggerError({ ...base, connectorId: connectorID, repo: 'octo/timothy', events: ['pr.opened'] })).toBeUndefined()
  })

  it('needs a webhook secret name and value before checking filter paths', () => {
    const hook = { ...newTriggerDraft(), kind: 'webhook' as const }
    expect(triggerError(hook)).toBe('Enter the signing secret name.')
    const named = { ...hook, credentialRef: 'K', secretValue: 'v' }
    expect(triggerError({ ...named, filters: [{ path: '', equals: 'x' }] })).toBe('Fix the filter paths.')
    expect(triggerError({ ...named, filters: [{ path: 'a b', equals: 'x' }] })).toBe('Fix the filter paths.')
    expect(triggerError({ ...named, filters: [{ path: '$.pull_request.user.login', equals: 'x' }] })).toBeUndefined()
  })

  it('leaves cron and manual rows as before', () => {
    expect(triggerError(newTriggerDraft())).toBeUndefined()
    expect(triggerError({ ...newTriggerDraft(), expr: 'nope' })).toMatch(/five fields/)
    expect(triggerError({ ...newTriggerDraft(), kind: 'manual' })).toBeUndefined()
  })

  it('repoError and filterPathError stay quiet on empty input', () => {
    expect(repoError(base)).toBeUndefined()
    expect(filterPathError('')).toBeUndefined()
    expect(filterPathError('action')).toBeUndefined()
    expect(filterPathError('a..b')).toBeTruthy()
  })
})

describe('githubConnectorOptions', () => {
  it('keeps enabled github connectors and the picked one', () => {
    const list = [
      connector({ id: 'a', name: 'gh-on' }),
      connector({ id: 'b', name: 'gh-off', enabled: false }),
      connector({ id: 'c', name: 'mail', kind: 'google' }),
      connector({ id: 'd', name: 'gh-picked-off', enabled: false }),
    ]
    expect(githubConnectorOptions(list, '').map((c) => c.id)).toEqual(['a'])
    expect(githubConnectorOptions(list, 'd').map((c) => c.id)).toEqual(['a', 'd'])
  })
})

describe('goalHint', () => {
  it('switches with the trigger kinds and keeps the notes hint', () => {
    const cron = newTriggerDraft()
    expect(goalHint([cron])).toBe('Use {{event.pr_url}} or {{notes.name}} to insert trigger data or notes.')
    const gh = goalHint([cron, { ...cron, kind: 'connector_event' }])
    expect(gh).toContain('{{event.repo}}')
    expect(gh).toContain('{{event.author}}')
    expect(gh).not.toContain('{{event.delivery}}')
    expect(gh).toContain('{{notes.name}}')
    const hook = goalHint([{ ...cron, kind: 'webhook' }])
    expect(hook).toContain('{{event.body.<field>}}')
    expect(hook).toContain('{{event.delivery}}')
    expect(hook).not.toContain('{{event.repo}}')
  })
})

describe('channel drafts', () => {
  const channelID = '0000000c-0000-0000-0000-00000000c001'
  const stored = {
    id: 't7',
    kind: 'channel' as const,
    config: { channel_id: channelID, pattern: '^/run coverage$', chat_id: '-100' },
    enabled: true,
  }

  it('loads the config and keeps the id', () => {
    const [d] = draftsFromTriggers([stored])
    expect(d).toMatchObject({ id: 't7', kind: 'channel', channelId: channelID, pattern: '^/run coverage$', chatId: '-100' })
  })

  it('round-trips to the wire shape without a credential_ref', () => {
    expect(draftsToInput(draftsFromTriggers([stored]))).toEqual([
      { id: 't7', kind: 'channel', config: { channel_id: channelID, pattern: '^/run coverage$', chat_id: '-100' }, enabled: true },
    ])
  })

  it('omits an empty chat id and keeps the pattern as typed', () => {
    const d = { ...newTriggerDraft(), kind: 'channel' as const, channelId: channelID, pattern: ' ^deploy ', chatId: '  ' }
    expect(draftsToInput([d])[0].config).toEqual({ channel_id: channelID, pattern: ' ^deploy ' })
  })

  it('walks channel, pattern and pattern validity in order', () => {
    const base = { ...newTriggerDraft(), kind: 'channel' as const }
    expect(triggerError(base)).toBe('Pick a channel.')
    expect(triggerError({ ...base, channelId: channelID })).toBe('Enter a pattern.')
    expect(triggerError({ ...base, channelId: channelID, pattern: '(unclosed' })).toBe('Not a valid regular expression.')
    expect(triggerError({ ...base, channelId: channelID, pattern: 'a'.repeat(201) })).toContain('200 characters')
    expect(triggerError({ ...base, channelId: channelID, pattern: '(?i)^/run' })).toBeUndefined()
    expect(patternError({ ...base, pattern: '' })).toBeUndefined()
  })

  it('adds the channel placeholders to the goal hint', () => {
    const hint = goalHint([{ ...newTriggerDraft(), kind: 'channel' }])
    expect(hint).toContain('{{event.text}}')
    expect(hint).toContain('{{event.sender}}')
    expect(hint).toContain('{{event.chat_id}}')
  })
})
