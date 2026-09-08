import { describe, expect, it } from 'vitest'
import { agentPhaseFromState, consequenceLine, humanizeTool, toolCategory } from './chatUi'
import { emptyAssistant } from './chat'
import type { PermissionRequestEvent } from '@/api/types'

describe('toolCategory', () => {
  it('categorizes file tools', () => {
    expect(toolCategory('read_file')).toBe('file')
  })

  it('categorizes shell tools', () => {
    expect(toolCategory('run_command')).toBe('shell')
  })

  it('categorizes web tools', () => {
    expect(toolCategory('search_web')).toBe('web')
  })

  it('categorizes mail tools', () => {
    expect(toolCategory('send_mail')).toBe('mail')
  })

  it('categorizes calendar tools', () => {
    expect(toolCategory('list_calendar_events')).toBe('calendar')
  })

  it('categorizes memory tools', () => {
    expect(toolCategory('memory_recall')).toBe('memory')
  })

  it('categorizes kb tools', () => {
    expect(toolCategory('kb_search')).toBe('kb')
  })

  it('categorizes hyphenated/underscored connector tools', () => {
    expect(toolCategory('mcp_custom_tool')).toBe('connector')
  })

  it('falls back to other for unrecognized tools with no separators', () => {
    expect(toolCategory('somethingweird')).toBe('other')
  })
})

describe('humanizeTool', () => {
  it('humanizes the tool name and returns no target when there are no args', () => {
    expect(humanizeTool({ id: '1', name: 'read_file', status: 'ok' })).toEqual({
      action: 'Read file',
    })
  })

  it('returns just the action when args fail to parse as JSON', () => {
    expect(humanizeTool({ id: '1', name: 'read_file', status: 'ok', args: 'not json' })).toEqual({
      action: 'Read file',
    })
  })

  it('returns just the action when args parse to a non-object', () => {
    expect(humanizeTool({ id: '1', name: 'read_file', status: 'ok', args: '42' })).toEqual({
      action: 'Read file',
    })
  })

  it('extracts a target from a known key', () => {
    expect(
      humanizeTool({ id: '1', name: 'read_file', status: 'ok', args: '{"path":"/tmp/x.txt"}' }),
    ).toEqual({ action: 'Read file', target: '/tmp/x.txt' })
  })

  it('truncates a long target value', () => {
    const long = 'a'.repeat(80)
    const result = humanizeTool({
      id: '1',
      name: 'read_file',
      status: 'ok',
      args: `{"path":"${long}"}`,
    })
    expect(result.target).toBe(`${'a'.repeat(60)}…`)
  })

  it('returns just the action when no target key matches', () => {
    expect(
      humanizeTool({ id: '1', name: 'read_file', status: 'ok', args: '{"other":"value"}' }),
    ).toEqual({ action: 'Read file' })
  })
})

describe('consequenceLine', () => {
  const base = (overrides: Partial<PermissionRequestEvent>): PermissionRequestEvent => ({
    id: '1',
    call_id: '1',
    tool: 'read_file',
    args: '{}',
    danger_level: 'safe',
    rationale: '',
    ...overrides,
  })

  it('marks a destructive request', () => {
    expect(consequenceLine(base({ danger_level: 'destructive', tool: 'write_file' }))).toBe(
      'Marked destructive by the tool policy: this can change or delete data and may not be reversible.',
    )
  })

  it('describes a safe request as reversible', () => {
    expect(consequenceLine(base({ tool: 'read_file' }))).toBe(
      "Runs on the Timothy server with this session's permissions and is expected to be reversible.",
    )
  })

  it('appends a shell-command note for shell tools', () => {
    expect(consequenceLine(base({ tool: 'run_command' }))).toContain('Executes a shell command.')
  })

  it('appends an outside-traffic note for web tools', () => {
    expect(consequenceLine(base({ tool: 'search_web' }))).toContain(
      'Sends traffic outside Timothy.',
    )
  })

  it('appends an outside-traffic note for mail tools', () => {
    expect(consequenceLine(base({ tool: 'send_mail' }))).toContain(
      'Sends traffic outside Timothy.',
    )
  })

  it('appends an outside-traffic note for connector tools', () => {
    expect(consequenceLine(base({ tool: 'mcp_custom' }))).toContain(
      'Sends traffic outside Timothy.',
    )
  })

  it('leaves the base line unchanged for other categories', () => {
    expect(consequenceLine(base({ tool: 'memory_recall' }))).toBe(
      "Runs on the Timothy server with this session's permissions and is expected to be reversible.",
    )
  })
})

describe('agentPhaseFromState', () => {
  it('returns idle when there is no message', () => {
    expect(agentPhaseFromState(null, { pendingPermission: false })).toEqual({ kind: 'idle' })
  })

  it('returns idle when the message is not streaming', () => {
    const msg = { ...emptyAssistant(), streaming: false }
    expect(agentPhaseFromState(msg, { pendingPermission: false })).toEqual({ kind: 'idle' })
  })

  it('returns waiting for approval when a permission is pending', () => {
    const msg = emptyAssistant()
    expect(agentPhaseFromState(msg, { pendingPermission: true })).toEqual({
      kind: 'waiting',
      what: 'approval',
    })
  })

  it('returns error when the message carries an error', () => {
    const msg = { ...emptyAssistant(), error: 'boom' }
    expect(agentPhaseFromState(msg, { pendingPermission: false })).toEqual({
      kind: 'error',
      message: 'boom',
    })
  })

  it('returns tool when a tool call is running', () => {
    const msg = {
      ...emptyAssistant(),
      tools: [{ id: '1', name: 'read_file', status: 'running' as const }],
    }
    expect(agentPhaseFromState(msg, { pendingPermission: false })).toEqual({
      kind: 'tool',
      tool: 'read_file',
    })
  })

  it('returns streaming when text has arrived', () => {
    const msg = { ...emptyAssistant(), text: 'hello' }
    expect(agentPhaseFromState(msg, { pendingPermission: false })).toEqual({ kind: 'streaming' })
  })

  it('returns thinking when nothing else applies', () => {
    const msg = emptyAssistant()
    expect(agentPhaseFromState(msg, { pendingPermission: false })).toEqual({ kind: 'thinking' })
  })
})
