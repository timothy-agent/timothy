import { describe, expect, it } from 'vitest'
import { followUpGoal, parseOptionShortlist } from './optionShortlist'

function option(name: string, pitch: string): string {
  return [
    `### ${name}`,
    `**Pitch:** ${pitch}`,
    '**Fit:** satisfies R1 and R4.',
    '**Differentiation:** closest is [Thing](https://example.com/thing), crowded.',
    '**Build:** Go service plus a React panel.',
    '**Demo:** three beats ending on the graph.',
    '**Why choose it:** the judges asked for exactly this.',
    '**Why not:** the ingest path may not finish in time.',
    '',
  ].join('\n')
}

describe('parseOptionShortlist', () => {
  it('parses a multi-option document with a dropped section', () => {
    const text = [
      'Ranked against R1-R9.',
      '',
      option('Signal Router', 'Routes alerts to the right on-call human.'),
      option('Ledger Lens', 'Shows where the token spend actually went.'),
      '## Dropped',
      '- **Prompt Wrapper:** thin wrapper, no agent work.',
      '- Chat over PDFs: crowded.',
      '- violates R3 (team size)',
    ].join('\n')

    const parsed = parseOptionShortlist(text)
    expect(parsed).not.toBeNull()
    expect(parsed!.options.map((o) => o.name)).toEqual(['Signal Router', 'Ledger Lens'])
    expect(parsed!.options[0].pitch).toBe('Routes alerts to the right on-call human.')
    expect(parsed!.options[0].fields.map((f) => f.label)).toEqual([
      'Pitch',
      'Fit',
      'Differentiation',
      'Build',
      'Demo',
      'Why choose it',
      'Why not',
    ])
    expect(parsed!.options[0].fields[3].text).toBe('Go service plus a React panel.')
    expect(parsed!.options[0].rawMarkdown.startsWith('### Signal Router')).toBe(true)
    expect(parsed!.dropped!.entries).toEqual([
      { name: 'Prompt Wrapper', reason: 'thin wrapper, no agent work.' },
      { name: 'Chat over PDFs', reason: 'crowded.' },
      { reason: 'violates R3 (team size)' },
    ])
  })

  it('parses a document with no dropped section', () => {
    const parsed = parseOptionShortlist(option('Only One', 'A single idea.'))
    expect(parsed!.options).toHaveLength(1)
    expect(parsed!.dropped).toBeUndefined()
  })

  it('tolerates label casing and spacing differences', () => {
    const text = option('Loose', 'Loose labels.')
      .replace('**Pitch:**', '**pitch:**')
      .replace('**Why choose it:**', '**Why  Choose It:**')
    const parsed = parseOptionShortlist(text)
    expect(parsed!.options[0].pitch).toBe('Loose labels.')
  })

  it('folds a wrapped field continuation into the field text', () => {
    const text = option('Wrapped', 'Line one.').replace(
      '**Build:** Go service plus a React panel.',
      '**Build:** Go service\nplus a React panel.',
    )
    const parsed = parseOptionShortlist(text)
    expect(parsed!.options[0].fields[3].text).toBe('Go service\nplus a React panel.')
  })

  it('does not match prose with an unrelated level-3 heading', () => {
    const text = ['# Report', '', '### Background', '', 'Some prose about the problem.'].join('\n')
    expect(parseOptionShortlist(text)).toBeNull()
  })

  it('does not match a section missing a required label', () => {
    const text = option('Partial', 'Missing a label.').replace(
      '**Why not:** the ingest path may not finish in time.\n',
      '',
    )
    expect(parseOptionShortlist(text)).toBeNull()
  })

  it('does not match an empty document', () => {
    expect(parseOptionShortlist('')).toBeNull()
    expect(parseOptionShortlist('   \n\n')).toBeNull()
  })

  it('ignores headings inside fenced code blocks', () => {
    const text = ['# Doc', '', '```md', option('Fenced', 'Inside a fence.'), '```'].join('\n')
    expect(parseOptionShortlist(text)).toBeNull()
  })
})

describe('followUpGoal', () => {
  it('formats the pitch as objective and the build line as scope', () => {
    const parsed = parseOptionShortlist(option('Signal Router', 'Routes alerts.'))
    expect(followUpGoal(parsed!.options[0])).toBe(
      [
        'Build "Signal Router".',
        '',
        'Objective: Routes alerts.',
        'Scope: Go service plus a React panel.',
      ].join('\n'),
    )
  })
})
