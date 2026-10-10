import { describe, expect, it } from 'vitest'
import {
  appendAllowlist,
  buildArgs,
  indexNote,
  mcpTokenRef,
  parseMCPInput,
  schemaFields,
  stdioReason,
} from './mcpAddFlow'

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
      name: 'mcpServers with an HTTP and a stdio entry',
      input: JSON.stringify({
        mcpServers: {
          Notion: { url: 'https://mcp.notion.com/mcp' },
          local: { command: 'npx', args: ['-y', 'some-server'] },
        },
      }),
      endpoints: ['https://mcp.notion.com/mcp'],
      names: ['notion'],
      unsupported: [{ name: 'local', reason: stdioReason }],
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

describe('mcpTokenRef', () => {
  it.each([
    ['notion', 'NOTION_MCP_TOKEN'],
    ['notion-mcp', 'NOTION_MCP_TOKEN'],
    ['My Server', 'MY_SERVER_MCP_TOKEN'],
  ])('%s -> %s', (name, want) => {
    expect(mcpTokenRef(name)).toBe(want)
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
