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

function renderStep(missionFloor?: string[]) {
  return render(
    <MemoryRouter>
      <RolesStep missionFloor={missionFloor} onBack={() => {}} onNext={() => {}} />
    </MemoryRouter>,
  )
}

const floorHint = 'This model can chat but cannot run missions. Pick a stronger model for missions in Settings.'

beforeEach(() => vi.clearAllMocks())

describe('RolesStep', () => {
  it('renders one plain line per role', async () => {
    vi.mocked(listRoutes).mockResolvedValue([
      route('default', {
        chain: [{ provider_id: 'p1', model: 'qwen3:8b' }],
        serving: { provider_id: 'p1', model: 'qwen3:8b' },
        resolved: [{ provider_id: 'p1', provider_name: 'Ollama', model: 'qwen3:8b', usable: true }],
      }),
      route('summarize', { chain: [{ provider_id: 'p1', model: 'llama3.2' }] }),
      route('embedding', { chain: [{ provider_id: 'p1', model: 'nomic-embed-text' }] }),
      route('vision'),
    ])
    renderStep()
    expect(await screen.findByText('Chat answers with qwen3:8b from Ollama')).toBeInTheDocument()
    expect(screen.getByText('Summaries use llama3.2')).toBeInTheDocument()
    expect(screen.getByText('Memory and knowledge search use nomic-embed-text')).toBeInTheDocument()
    expect(screen.getByText('Images: no model yet, add a provider with a vision model later')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Change in Settings' })).toHaveAttribute('href', '/settings/routes')
  })

  it.each([
    ['qwen2.5:7b', ['qwen2.5:7b', 'nova'], true],
    ['amazon.nova-lite-v1:0', ['qwen2.5:7b', 'nova'], true],
    ['qwen3:8b', ['qwen2.5:7b', 'nova'], false],
    ['qwen2.5:7b', undefined, false],
  ])('chat model %s with floor %j shows the mission hint: %s', async (model, floor, want) => {
    vi.mocked(listRoutes).mockResolvedValue([route('default', { chain: [{ provider_id: 'p1', model }] })])
    renderStep(floor)
    await screen.findByText(`Chat answers with ${model}`)
    expect(screen.queryByText(floorHint) !== null).toBe(want)
  })

  it('never hints for a weak model outside the chat line', async () => {
    vi.mocked(listRoutes).mockResolvedValue([
      route('default', { chain: [{ provider_id: 'p1', model: 'qwen3:8b' }] }),
      route('summarize', { chain: [{ provider_id: 'p1', model: 'qwen2.5:7b' }] }),
    ])
    renderStep(['qwen2.5:7b'])
    await screen.findByText('Summaries use qwen2.5:7b')
    expect(screen.queryByText(floorHint)).toBeNull()
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
