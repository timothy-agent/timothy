import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { listRoutes } from '../../api/client'
import type { AdminRoute } from '../../api/types'
import { RolesStep } from './RolesStep'

vi.mock('../../api/client', () => ({ listRoutes: vi.fn() }))

function route(role: string, extra: Partial<AdminRoute> = {}): AdminRoute {
  return { name: role, chain: [], strategy: 'ordered', enabled: true, role, ...extra }
}

function renderStep() {
  return render(
    <MemoryRouter>
      <RolesStep onBack={() => {}} onNext={() => {}} />
    </MemoryRouter>,
  )
}

beforeEach(() => vi.clearAllMocks())

describe('RolesStep', () => {
  it('renders one plain line per role', async () => {
    vi.mocked(listRoutes).mockResolvedValue([
      route('default', {
        chain: [{ provider_id: 'p1', model: 'qwen2.5:7b' }],
        serving: { provider_id: 'p1', model: 'qwen2.5:7b' },
        resolved: [{ provider_id: 'p1', provider_name: 'Ollama', model: 'qwen2.5:7b', usable: true }],
      }),
      route('summarize', { chain: [{ provider_id: 'p1', model: 'llama3.2' }] }),
      route('embedding', { chain: [{ provider_id: 'p1', model: 'nomic-embed-text' }] }),
      route('vision'),
    ])
    renderStep()
    expect(await screen.findByText('Chat answers with qwen2.5:7b from Ollama')).toBeInTheDocument()
    expect(screen.getByText('Summaries use llama3.2')).toBeInTheDocument()
    expect(screen.getByText('Memory and knowledge search use nomic-embed-text')).toBeInTheDocument()
    expect(screen.getByText('Images: no model yet, add a provider with a vision model later')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Change in Settings' })).toHaveAttribute('href', '/settings/routes')
  })

  it('shows a vision model when one is set', async () => {
    vi.mocked(listRoutes).mockResolvedValue([route('vision', { chain: [{ provider_id: 'p2', model: 'gpt-4o' }] })])
    renderStep()
    expect(await screen.findByText('Images: gpt-4o')).toBeInTheDocument()
  })

  it('never says route', async () => {
    vi.mocked(listRoutes).mockResolvedValue([])
    const { container } = renderStep()
    await screen.findByText('Chat: no model yet')
    expect(container.textContent?.toLowerCase()).not.toContain('route')
  })
})
