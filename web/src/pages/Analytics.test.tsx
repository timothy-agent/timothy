import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { BudgetStatus, UsageSummary } from '../api/types'
import { Analytics } from './Analytics'

vi.mock('../api/client', () => ({
  usageBudget: vi.fn(),
  usageCache: vi.fn(),
  usageLatency: vi.fn(),
  usageSeries: vi.fn(),
  usageSessions: vi.fn(),
  usageSummary: vi.fn(),
}))

import {
  usageBudget,
  usageCache,
  usageLatency,
  usageSeries,
  usageSessions,
  usageSummary,
} from '../api/client'

const summary: UsageSummary = {
  cost_usd: 2.5,
  input_tokens: 1000,
  output_tokens: 500,
  cache_read_tokens: 0,
  cache_write_tokens: 0,
  requests: 10,
  errors: 0,
  unpriced_requests: 0,
  unpriced_input_tokens: 0,
  unpriced_output_tokens: 0,
}

const calmBudget: BudgetStatus = {
  day: { limit_usd: 10, spend_usd: 2.5, over: false },
  month: { limit_usd: null, spend_usd: 2.5, over: false },
}

function renderPage() {
  return render(
    <MemoryRouter>
      <Analytics />
    </MemoryRouter>,
  )
}

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(usageSummary).mockResolvedValue(summary)
  vi.mocked(usageSeries).mockResolvedValue([])
  vi.mocked(usageSessions).mockResolvedValue([])
  vi.mocked(usageLatency).mockResolvedValue([])
  vi.mocked(usageCache).mockResolvedValue([])
  vi.mocked(usageBudget).mockResolvedValue(calmBudget)
})

describe('Analytics budget alert', () => {
  it('shows no banner while spend stays under budget', async () => {
    renderPage()
    await waitFor(() => expect(screen.getAllByText('$2.50').length).toBeGreaterThan(0))
    expect(screen.queryByRole('alert')).toBeNull()
    // A configured limit still surfaces as a tile hint.
    expect(screen.getByText('of $10.00 budget')).toBeInTheDocument()
  })

  it('shows a banner naming every window over budget', async () => {
    vi.mocked(usageBudget).mockResolvedValue({
      day: { limit_usd: 1, spend_usd: 1.5, over: true },
      month: { limit_usd: 100, spend_usd: 120, over: true },
    })
    renderPage()
    const banner = await screen.findByRole('alert')
    expect(banner).toHaveTextContent('Daily budget reached: $1.50 spent of $1.00.')
    expect(banner).toHaveTextContent('Monthly budget reached: $120.00 spent of $100.00.')
  })

  it('stays silent when the budget endpoint fails', async () => {
    vi.mocked(usageBudget).mockRejectedValue(new Error('gateway down'))
    renderPage()
    // The shared widget-failure note appears; no budget banner.
    await screen.findByText(/widgets failed to load/)
    expect(screen.queryByRole('alert')).toBeNull()
  })
})

describe('Analytics token tiles', () => {
  it('shows total input and output tokens for the range', async () => {
    vi.mocked(usageSummary).mockResolvedValue({
      ...summary,
      input_tokens: 1_250_000,
      output_tokens: 84_000,
      cache_read_tokens: 400_000,
    })
    renderPage()
    expect(await screen.findByText('Input tokens')).toBeTruthy()
    expect(screen.getByText('1.3M')).toBeTruthy()
    expect(screen.getByText('Output tokens')).toBeTruthy()
    expect(screen.getByText('84.0k')).toBeTruthy()
    expect(screen.getByText('400.0k cached reads')).toBeTruthy()
  })
})

describe('Analytics unpriced usage', () => {
  it('notes unpriced calls with a catalog estimate', async () => {
    vi.mocked(usageSummary).mockResolvedValue({ ...summary, unpriced_requests: 4 })
    // gpt-5.6-sol prices in the catalog: $5/mtok in, $30/mtok out.
    vi.mocked(usageSeries).mockResolvedValue([
      {
        bucket: '2026-07-24T00:00:00Z',
        group: 'gpt-5.6-sol',
        cost_usd: 0,
        input_tokens: 1_000_000,
        output_tokens: 100_000,
        requests: 4,
        errors: 0,
        unpriced_input_tokens: 1_000_000,
        unpriced_output_tokens: 100_000,
      },
    ])
    renderPage()
    const note = await screen.findByText(/had no configured price/)
    expect(note).toHaveTextContent('≈$8.00 at catalog prices')
  })

  it('stays silent when every call is priced', async () => {
    renderPage()
    await waitFor(() => expect(screen.getAllByText('$2.50').length).toBeGreaterThan(0))
    expect(screen.queryByText(/had no configured price/)).toBeNull()
  })
})
