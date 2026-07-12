import { describe, expect, it } from 'vitest'
import { splitSources } from './citations'

describe('splitSources', () => {
  it('splits a trailing Sources section into structured citations', () => {
    const text = [
      'Bedrock Nova Lite costs $0.06 per 1M input tokens.',
      '',
      '## Sources',
      '1. [AWS Bedrock Pricing](https://aws.amazon.com/bedrock/pricing/)',
      '2. [Nova Model Card](https://aws.amazon.com/ai/generative-ai/nova/)',
    ].join('\n')

    const { body, citations } = splitSources(text)
    expect(body).toBe('Bedrock Nova Lite costs $0.06 per 1M input tokens.')
    expect(citations).toEqual([
      { title: 'AWS Bedrock Pricing', url: 'https://aws.amazon.com/bedrock/pricing/' },
      { title: 'Nova Model Card', url: 'https://aws.amazon.com/ai/generative-ai/nova/' },
    ])
  })

  it('returns the text unchanged when there is no Sources section', () => {
    const text = 'Just an answer, no citations.'
    expect(splitSources(text)).toEqual({ body: text, citations: [] })
  })

  it('is case-insensitive on the heading', () => {
    const text = '# answer\n\n## sources\n1. [X](https://x.com)'
    const { citations } = splitSources(text)
    expect(citations).toHaveLength(1)
  })

  it('ignores a Sources heading with no parseable list under it', () => {
    const text = 'Answer text.\n\n## Sources\nI did not cite anything specific.'
    const { body, citations } = splitSources(text)
    expect(citations).toEqual([])
    expect(body).toBe(text)
  })

  it('only treats the last Sources heading as the citation block', () => {
    const text = [
      'This section discusses ## Sources of error in measurement.',
      '',
      'The actual answer follows.',
      '',
      '## Sources',
      '1. [Ref](https://example.com)',
    ].join('\n')
    const { citations } = splitSources(text)
    expect(citations).toEqual([{ title: 'Ref', url: 'https://example.com' }])
  })
})
