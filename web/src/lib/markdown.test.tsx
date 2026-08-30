import { cleanup, render, screen } from '@testing-library/react'
import ReactMarkdown from 'react-markdown'
import { afterEach, describe, expect, it } from 'vitest'
import { rehypePlugins, remarkPlugins } from './markdown'

afterEach(cleanup)

function renderMarkdown(text: string) {
  return render(
    <ReactMarkdown remarkPlugins={remarkPlugins} rehypePlugins={rehypePlugins}>
      {text}
    </ReactMarkdown>,
  )
}

describe('shared markdown config', () => {
  it('renders raw HTML img tags', () => {
    renderMarkdown('<img src="https://example.com/x.png" alt="x">')
    const img = screen.getByAltText('x')
    expect(img).toHaveAttribute('src', 'https://example.com/x.png')
  })

  it('strips script tags', () => {
    const { container } = renderMarkdown('<script>alert(1)</script>')
    expect(container.querySelector('script')).toBeNull()
  })

  it('strips event handler attributes like onerror', () => {
    const { container } = renderMarkdown('<img src=x onerror="alert(1)">')
    const img = container.querySelector('img')
    expect(img).not.toBeNull()
    expect(img?.getAttribute('onerror')).toBeNull()
  })

  it('strips javascript: hrefs', () => {
    const { container } = renderMarkdown('[click me](javascript:alert(1))')
    const link = container.querySelector('a')
    expect(link).not.toBeNull()
    expect(link?.getAttribute('href')).toBeNull()
  })

  it('still tags fenced code blocks with a language-* className', () => {
    const { container } = renderMarkdown('```typescript\nconst x = 1\n```')
    const code = container.querySelector('code')
    expect(code?.className).toContain('language-typescript')
  })
})
