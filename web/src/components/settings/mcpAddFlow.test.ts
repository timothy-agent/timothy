import { describe, expect, it } from 'vitest'
import type { AdminConnector, ConnectorProbe } from '../../api/types'
import {
  allowedBy,
  allowlistName,
  catalogProvider,
  appendAllowlist,
  buildArgs,
  indexNote,
  lastProbeFrom,
  lastProbeOf,
  missionReadsOf,
  parseMCPInput,
  providerPattern,
  schemaFields,
  commandLine,
  envFor,
  envNameError,
  localServerOf,
  manualLocalCandidate,
} from './mcpAddFlow'
import { mcpEnvRef } from './credentialRefs'

describe('parseMCPInput', () => {
  it.each([
    {
      name: 'bare URL',
      input: '  https://mcp.example.com/mcp ',
      endpoints: ['https://mcp.example.com/mcp'],
      names: [''],
      unsupported: [],
    },
    {
      name: 'mcpServers with an HTTP and a local entry',
      input: JSON.stringify({
        mcpServers: {
          Notion: { url: 'https://mcp.notion.com/mcp' },
          local: { command: 'npx', args: ['-y', 'some-server'] },
        },
      }),
      endpoints: ['https://mcp.notion.com/mcp', ''],
      names: ['notion', 'local'],
      unsupported: [],
    },
    {
      name: 'inner name-to-entry object',
      input: JSON.stringify({ linear: { url: 'https://mcp.linear.app/mcp' } }),
      endpoints: ['https://mcp.linear.app/mcp'],
      names: ['linear'],
      unsupported: [],
    },
    {
      name: 'serverUrl variant',
      input: JSON.stringify({ mcpServers: { sentry: { serverUrl: 'https://mcp.sentry.dev/mcp' } } }),
      endpoints: ['https://mcp.sentry.dev/mcp'],
      names: ['sentry'],
      unsupported: [],
    },
    {
      name: 'single entry',
      input: JSON.stringify({ endpoint: 'https://mcp.example.com/mcp' }),
      endpoints: ['https://mcp.example.com/mcp'],
      names: [''],
      unsupported: [],
    },
    {
      name: 'entry without a URL',
      input: JSON.stringify({ mcpServers: { odd: { url: 'ftp://x' } } }),
      endpoints: [],
      names: [],
      unsupported: [{ name: 'odd', reason: 'No http or https URL in this entry.' }],
    },
  ])('$name', ({ input, endpoints, names, unsupported }) => {
    const got = parseMCPInput(input)
    expect(got.error).toBeUndefined()
    expect(got.candidates.map((c) => c.endpoint)).toEqual(endpoints)
    expect(got.candidates.map((c) => c.name)).toEqual(names)
    expect(got.unsupported).toEqual(unsupported)
  })

  it.each([
    { name: 'malformed JSON', input: '{"mcpServers": ', error: 'Paste an http(s) URL or a JSON config.' },
    { name: 'JSON array', input: '[1]', error: 'The JSON must be an object.' },
    { name: 'empty servers', input: '{"mcpServers": {}}', error: 'No servers found in this JSON.' },
  ])('$name reports an error', ({ input, error }) => {
    expect(parseMCPInput(input).error).toBe(error)
  })

  it('returns nothing for empty input', () => {
    expect(parseMCPInput('   ')).toEqual({ candidates: [], unsupported: [] })
  })

  it('lifts a literal bearer into the token and flags templated headers', () => {
    const got = parseMCPInput(
      JSON.stringify({
        mcpServers: {
          a: { url: 'https://a.example.com/mcp', headers: { Authorization: 'Bearer sk-live-1', 'X-Team': 'core' } },
          b: { url: 'https://b.example.com/mcp', headers: { Authorization: 'Bearer ${B_TOKEN}', 'X-Api-Key': '<your key>' } },
        },
      }),
    )
    expect(got.candidates[0]).toMatchObject({ token: 'sk-live-1', headers: { 'X-Team': 'core' }, placeholders: [] })
    expect(got.candidates[1]).toMatchObject({ token: '', headers: { 'X-Api-Key': '<your key>' }, placeholders: ['X-Api-Key'] })
  })
})

describe('appendAllowlist', () => {
  it('appends new names in order and skips ones already present', () => {
    expect(appendAllowlist(['shell', 'search'], ['search', 'notion_fetch', 'create_page'])).toEqual([
      'shell',
      'search',
      'notion_fetch',
      'create_page',
    ])
  })
})

describe('indexNote', () => {
  it.each([
    [3, 8, 'within the MCP tool index threshold of 8'],
    [9, 8, 'above the MCP tool index threshold of 8'],
    [40, 0, 'The tool index is off'],
  ])('%i tools, threshold %i', (count, threshold, want) => {
    expect(indexNote(count, threshold)).toContain(want)
  })
})

describe('Try it schema form', () => {
  const schema = {
    type: 'object',
    required: ['query', 'filter'],
    properties: {
      query: { type: 'string', description: 'Search text' },
      limit: { type: 'integer' },
      sort: { enum: ['asc', 'desc'] },
      archived: { type: 'boolean' },
      tags: { type: 'array', items: { type: 'string' } },
      filter: {
        type: 'object',
        required: ['status'],
        properties: { status: { type: 'string', enum: ['open', 'closed'] }, owner: { type: 'string' } },
      },
      meta: { type: 'object', properties: { team: { type: 'string' } }, required: ['team'] },
    },
  }

  it('flattens nested objects and maps kinds', () => {
    const fields = schemaFields(schema)
    expect(fields.map((f) => [f.path.join('.'), f.kind, f.required])).toEqual([
      ['query', 'string', true],
      ['limit', 'integer', false],
      ['sort', 'enum', false],
      ['archived', 'boolean', false],
      ['tags', 'json', false],
      ['filter.status', 'enum', true],
      ['filter.owner', 'string', false],
      // required inside an optional parent stays optional
      ['meta.team', 'string', false],
    ])
    expect(fields[0].description).toBe('Search text')
  })

  it('builds nested arguments and leaves empty optional fields out', () => {
    const { args, errors } = buildArgs(schemaFields(schema), {
      query: 'bugs',
      limit: '5',
      'filter.status': 'open',
      tags: '["a","b"]',
      archived: true,
    })
    expect(errors).toEqual({})
    expect(args).toEqual({ query: 'bugs', limit: 5, archived: true, tags: ['a', 'b'], filter: { status: 'open' } })
  })

  it('reports invalid and missing values per field', () => {
    const { errors } = buildArgs(schemaFields(schema), {
      limit: '2.5',
      sort: 'sideways',
      tags: '[oops',
    })
    expect(errors).toEqual({
      query: 'Required.',
      limit: 'Must be a whole number.',
      sort: 'Pick one of the listed values.',
      tags: 'Must be valid JSON.',
      'filter.status': 'Required.',
    })
  })

  it('keeps numeric enum values as numbers', () => {
    const fields = schemaFields({ type: 'object', properties: { level: { enum: [1, 2, 3] } } })
    expect(buildArgs(fields, { level: '2' }).args).toEqual({ level: 2 })
  })
})

describe('last probe record', () => {
  const probe: ConnectorProbe = {
    status: 'ok',
    server: { name: 'Notion', version: '1' },
    tool_count: 600,
    index_threshold: 8,
    tools: [
      { name: 'search', final_name: 'search', description: 'Search', read_only_hint: true, input_schema: { type: 'object' } },
      { name: 'shell', final_name: 'notion_shell', description: 'Clash', read_only_hint: null, input_schema: {} },
      { name: 'plain', description: 'No final name', read_only_hint: false, input_schema: {} },
    ],
  }

  it('lastProbeFrom keeps the table columns only', () => {
    const at = new Date('2026-10-11T12:00:00Z')
    expect(lastProbeFrom(probe, at)).toEqual({
      at: '2026-10-11T12:00:00.000Z',
      tool_count: 600,
      tools: [
        { name: 'search', final_name: 'search', read_only_hint: true },
        { name: 'shell', final_name: 'notion_shell', read_only_hint: null },
        { name: 'plain', read_only_hint: false },
      ],
    })
  })

  it('lastProbeOf round-trips and rejects malformed records', () => {
    const base = { id: 'm1', name: 'notion', kind: 'mcp' as const, credential_ref: '', enabled: true, sensitive: false }
    const rec = lastProbeFrom(probe, new Date('2026-10-11T12:00:00Z'))
    expect(lastProbeOf({ ...base, config: { endpoint: 'https://x', last_probe: rec } })).toEqual(rec)
    expect(lastProbeOf({ ...base, config: { endpoint: 'https://x' } })).toBeNull()
    expect(lastProbeOf({ ...base, config: { last_probe: 'nope' } })).toBeNull()
    expect(lastProbeOf({ ...base, config: { last_probe: { at: '2026-10-11T12:00:00Z', tools: [{ nope: 1 }, { name: 'ok' }] } } })).toEqual({
      at: '2026-10-11T12:00:00Z',
      tool_count: 1,
      tools: [{ name: 'ok', read_only_hint: null }],
    })
  })

  it('lastProbeFrom carries the fingerprint when the probe has one', () => {
    const at = new Date('2026-10-11T12:00:00Z')
    expect(lastProbeFrom({ ...probe, hash: 'h1' }, at).hash).toBe('h1')
    expect('hash' in lastProbeFrom(probe, at)).toBe(false)
    const base = { id: 'm1', name: 'notion', kind: 'mcp' as const, credential_ref: '', enabled: true, sensitive: false }
    expect(lastProbeOf({ ...base, config: { last_probe: { at: 'x', tools: [], hash: 'h1' } } })?.hash).toBe('h1')
  })

  it('missionReadsOf reads the marks and rejects malformed records', () => {
    const base = { id: 'm1', name: 'notion', kind: 'mcp' as const, credential_ref: '', enabled: true, sensitive: false }
    expect(missionReadsOf({ ...base, config: {} })).toBeNull()
    expect(missionReadsOf({ ...base, config: { mission_reads: { tools: ['a'] } } })).toBeNull()
    expect(missionReadsOf({ ...base, config: { mission_reads: { hash: 'h', tools: ['a', 2, 'b'] } } })).toEqual({
      hash: 'h',
      tools: ['a', 'b'],
    })
  })

  it('allowlistName prefers the final name', () => {
    expect(allowlistName({ name: 'shell', final_name: 'notion_shell', read_only_hint: null })).toBe('notion_shell')
    expect(allowlistName({ name: 'plain', read_only_hint: null })).toBe('plain')
  })
})

describe('provider naming helpers', () => {
  it('allowedBy follows the backend suffix rule', () => {
    expect(allowedBy(['notion_search'], 'notion_search')).toBe('notion_search')
    expect(allowedBy(['shell', 'search'], 'notion_search')).toBe('search')
    expect(allowedBy(['arch'], 'notion_search')).toBeUndefined()
    expect(allowedBy([], 'notion_search')).toBeUndefined()
  })

  it('catalogProvider names hosted tiles only', () => {
    expect(catalogProvider({ id: 'notion', kind: 'mcp' })).toBe('notion')
    expect(catalogProvider({ id: 'custom-mcp', kind: 'mcp' })).toBeUndefined()
    expect(catalogProvider({ id: 'gmail', kind: 'google' })).toBeUndefined()
  })

  it('providerPattern accepts slugs only', () => {
    expect(providerPattern.test('notion')).toBe(true)
    expect(providerPattern.test('google_analytics')).toBe(true)
    expect(providerPattern.test('Notion')).toBe(false)
    expect(providerPattern.test('notion-work')).toBe(false)
    expect(providerPattern.test('a'.repeat(33))).toBe(false)
  })
})

describe('local server entries (D-162)', () => {
  it('reads command, args and env, leaving template values empty', () => {
    const got = parseMCPInput(
      JSON.stringify({
        mcpServers: {
          Analytics: {
            command: 'pipx',
            args: ['run', 'analytics-mcp', 7],
            env: { GOOGLE_PROJECT_ID: 'my-project', API_KEY: '${API_KEY}', N: 3 },
          },
        },
      }),
    )
    expect(got.unsupported).toEqual([])
    expect(got.candidates).toEqual([
      {
        name: 'analytics',
        endpoint: '',
        headers: {},
        token: '',
        placeholders: [],
        command: 'pipx',
        args: ['run', 'analytics-mcp'],
        env: { GOOGLE_PROJECT_ID: 'my-project', API_KEY: '' },
      },
    ])
  })

  it('reads a single local entry', () => {
    const got = parseMCPInput(JSON.stringify({ command: 'uvx', args: ['srv'] }))
    expect(got.candidates.map((c) => c.command)).toEqual(['uvx'])
  })

  it.each([
    { name: 'non-string command', command: 7 },
    { name: 'empty command', command: '  ' },
  ])('reports a $name as unsupported', ({ command }) => {
    const got = parseMCPInput(JSON.stringify({ mcpServers: { odd: { command } } }))
    expect(got.candidates).toEqual([])
    expect(got.unsupported).toEqual([{ name: 'odd', reason: 'The command must be a string.' }])
  })

  it('builds a candidate from the Advanced form', () => {
    expect(manualLocalCandidate('  ', 'x', '')).toBeNull()
    expect(manualLocalCandidate(' npx ', '-y\n\n  some-server \n', 'A_KEY, B_KEY\nC_KEY')).toEqual({
      name: '',
      endpoint: '',
      headers: {},
      token: '',
      placeholders: [],
      command: 'npx',
      args: ['-y', 'some-server'],
      env: { A_KEY: '', B_KEY: '', C_KEY: '' },
    })
  })

  it.each([
    { name: 'API_KEY', error: undefined },
    { name: '_x1', error: undefined },
    { name: '1KEY', error: 'Letters, digits and underscores, not starting with a digit.' },
    { name: 'MY-KEY', error: 'Letters, digits and underscores, not starting with a digit.' },
    { name: 'PATH', error: 'PATH is set by Timothy.' },
    { name: 'HOME', error: 'HOME is set by Timothy.' },
    { name: 'MCP_BRIDGE_TOKEN', error: 'MCP_BRIDGE_TOKEN is set by Timothy.' },
  ])('envNameError($name)', ({ name, error }) => {
    expect(envNameError(name)).toBe(error)
  })

  it('collects trimmed env values for the listed names only', () => {
    expect(envFor(['A', 'B'], { A: ' a ', C: 'c' })).toEqual({ A: 'a', B: '' })
  })

  it('names env secrets without stuttering', () => {
    expect(mcpEnvRef('ANALYTICS', 'GOOGLE_PROJECT_ID')).toBe('ANALYTICS_MCP_ENV_GOOGLE_PROJECT_ID')
    expect(mcpEnvRef('ANALYTICS_MCP', 'api_key')).toBe('ANALYTICS_MCP_ENV_API_KEY')
  })

  it('reads a command connector config', () => {
    const base = { id: 'c', name: 'analytics', kind: 'mcp' as AdminConnector['kind'], credential_ref: '', enabled: true, sensitive: false }
    expect(localServerOf({ ...base, config: { endpoint: 'https://x' } })).toBeNull()
    expect(localServerOf({ ...base, kind: 'aws', config: { command: 'npx' } })).toBeNull()
    expect(
      localServerOf({ ...base, config: { command: 'pipx', args: ['run', 'analytics-mcp'], env_refs: { K: 'REF' }, internet: true } }),
    ).toEqual({ command: 'pipx', args: ['run', 'analytics-mcp'], envRefs: { K: 'REF' }, internet: true })
    expect(localServerOf({ ...base, config: { command: 'npx' } })).toEqual({
      command: 'npx',
      args: [],
      envRefs: {},
      internet: false,
    })
  })

  it('quotes arguments with spaces in the command line', () => {
    expect(commandLine('python3', ['-c', 'print(1)', 'a b', ''])).toBe('python3 -c print(1) "a b" ""')
  })
})
