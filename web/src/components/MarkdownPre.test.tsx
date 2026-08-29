import { cleanup, render, screen, waitFor } from '@testing-library/react'
import ReactMarkdown from 'react-markdown'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { MarkdownPre } from './MarkdownPre'

const renderMock = vi.fn()

vi.mock('mermaid', () => ({
  default: {
    initialize: vi.fn(),
    render: (id: string, code: string) => renderMock(id, code),
  },
}))

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe('MarkdownPre', () => {
  it('renders a mermaid fence as a diagram', async () => {
    renderMock.mockResolvedValue({ svg: '<svg data-testid="diagram" />' })
    render(
      <ReactMarkdown components={{ pre: MarkdownPre }}>
        {'```mermaid\ngraph TD; A-->B;\n```'}
      </ReactMarkdown>,
    )
    await waitFor(() => expect(screen.getByTestId('diagram')).toBeInTheDocument())
  })

  it('leaves a non-mermaid fence as a plain pre', () => {
    render(
      <ReactMarkdown components={{ pre: MarkdownPre }}>
        {'```js\nconst x = 1;\n```'}
      </ReactMarkdown>,
    )
    expect(screen.getByText('const x = 1;')).toBeInTheDocument()
    expect(renderMock).not.toHaveBeenCalled()
  })
})
