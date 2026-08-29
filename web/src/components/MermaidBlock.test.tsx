import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { MermaidBlock } from './MermaidBlock'

const renderMock = vi.fn()
const initializeMock = vi.fn()

vi.mock('mermaid', () => ({
  default: {
    initialize: (opts: unknown) => initializeMock(opts),
    render: (id: string, code: string) => renderMock(id, code),
  },
}))

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe('MermaidBlock', () => {
  it('renders the diagram svg once mermaid resolves it', async () => {
    renderMock.mockResolvedValue({ svg: '<svg data-testid="diagram" />' })
    render(<MermaidBlock code="graph TD; A-->B;" />)
    await waitFor(() => expect(screen.getByTestId('diagram')).toBeInTheDocument())
  })

  it('falls back to the source view with no crash on invalid syntax', async () => {
    renderMock.mockRejectedValue(new Error('parse error'))
    render(<MermaidBlock code="not a diagram" />)
    await waitFor(() => expect(screen.getByText('not a diagram')).toBeInTheDocument())
    expect(screen.queryByText('View source')).toBeNull()
  })

  it('toggles between diagram and source view', async () => {
    renderMock.mockResolvedValue({ svg: '<svg data-testid="diagram" />' })
    render(<MermaidBlock code="graph TD; A-->B;" />)
    await waitFor(() => expect(screen.getByTestId('diagram')).toBeInTheDocument())

    fireEvent.click(screen.getByText('View source'))
    expect(screen.getByText('graph TD; A-->B;')).toBeInTheDocument()

    fireEvent.click(screen.getByText('View diagram'))
    await waitFor(() => expect(screen.getByTestId('diagram')).toBeInTheDocument())
  })
})
