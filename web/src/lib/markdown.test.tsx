import { cleanup, render, screen } from '@testing-library/react'
import ReactMarkdown from 'react-markdown'
import { MemoryRouter } from 'react-router'
import { afterEach, describe, expect, it } from 'vitest'
import { markdownComponents, rehypePlugins, remarkPlugins } from './markdown'

afterEach(cleanup)

function renderMarkdown(text: string) {
  return render(
    <ReactMarkdown remarkPlugins={remarkPlugins} rehypePlugins={rehypePlugins}>
      {text}
    </ReactMarkdown>,
  )
}

// With the shared component overrides, inside a router (links need one).
function renderLinks(text: string) {
  return render(
    <MemoryRouter>
      <ReactMarkdown
        remarkPlugins={remarkPlugins}
        rehypePlugins={rehypePlugins}
        components={markdownComponents}
      >
        {text}
      </ReactMarkdown>
    </MemoryRouter>,
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

  it('renders a known relative link as an in-app link', () => {
    renderLinks('[Features](/settings/features)')
    const link = screen.getByRole('link', { name: /Features/ })
    expect(link).toHaveAttribute('href', '/settings/features')
    expect(link).toHaveAttribute('title', 'Open in Timothy')
    expect(link).not.toHaveAttribute('target')
    expect(link.querySelector('svg')).not.toBeNull()
  })

  it('renders an unknown relative link as plain text with the href', () => {
    const { container } = renderLinks('[Nowhere](/nope/page)')
    expect(container.querySelector('a')).toBeNull()
    expect(container).toHaveTextContent('Nowhere (/nope/page)')
  })

  it('opens absolute links in a new tab', () => {
    renderLinks('[Docs](https://timothy-agent.github.io/docs/)')
    const link = screen.getByRole('link', { name: 'Docs' })
    expect(link).toHaveAttribute('target', '_blank')
    expect(link).toHaveAttribute('rel', 'noopener noreferrer')
  })

  it('renders no anchor for scheme-relative hrefs', () => {
    const { container } = renderLinks('[x](//evil.example/settings)')
    expect(container.querySelector('a')).toBeNull()
  })

  it('rejects data: hrefs', () => {
    const { container } = renderLinks('<a href="data:text/html,x">click</a>')
    expect(container.querySelector('a')?.getAttribute('href') ?? null).toBeNull()
  })
})
