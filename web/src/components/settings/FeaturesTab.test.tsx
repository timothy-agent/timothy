import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { FeaturesTab } from './FeaturesTab'

vi.mock('../../api/client', () => ({
  getSettings: vi.fn(),
  listRoutes: vi.fn(),
  patchSettings: vi.fn(),
  patchSettingValues: vi.fn(),
}))

import { getSettings, listRoutes, patchSettingValues } from '../../api/client'

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(listRoutes).mockResolvedValue([])
  vi.mocked(patchSettingValues).mockResolvedValue(undefined)
})

describe('FeaturesTab review token ceiling', () => {
  it('shows the stored ceiling beside the run budget and saves an edit', async () => {
    vi.mocked(getSettings).mockResolvedValue({
      settings: {},
      values: { executor_run_budget_minutes: '90', mission_review_token_ceiling: '250000' },
    })
    render(<FeaturesTab />)
    const input = (await screen.findByLabelText('Review token ceiling')) as HTMLInputElement
    expect(input.value).toBe('250000')
    expect(screen.getByLabelText('Harness run budget minutes')).toBeTruthy()

    fireEvent.change(input, { target: { value: '0' } })
    fireEvent.click(within(input.closest('div.rounded-xl') as HTMLElement).getByText('Save'))
    await waitFor(() => expect(patchSettingValues).toHaveBeenCalledWith({ mission_review_token_ceiling: '0' }))
  })

  it('shows the default as a placeholder when unset', async () => {
    vi.mocked(getSettings).mockResolvedValue({ settings: {}, values: {} })
    render(<FeaturesTab />)
    const input = (await screen.findByLabelText('Review token ceiling')) as HTMLInputElement
    expect(input.value).toBe('')
    expect(input.placeholder).toBe('1500000')
  })
})
