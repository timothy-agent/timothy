import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { FeaturesTab } from './FeaturesTab'

// FeaturesTab now renders PageHeader's breadcrumb links, which need a
// Router context, so every render is wrapped in MemoryRouter.
function renderTab() {
  return render(
    <MemoryRouter>
      <FeaturesTab />
    </MemoryRouter>,
  )
}

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
    renderTab()
    const input = (await screen.findByRole('spinbutton', { name: 'Review token ceiling' })) as HTMLInputElement
    expect(input.value).toBe('250000')
    expect(screen.getByRole('spinbutton', { name: 'Harness run budget minutes' })).toBeTruthy()

    const region = screen.getByRole('region', { name: 'Review token ceiling' })
    const saveButton = within(region).getByRole('button', { name: 'Save' })
    expect(saveButton).toBeDisabled()

    fireEvent.change(input, { target: { value: '0' } })
    expect(saveButton).toBeEnabled()
    fireEvent.click(saveButton)
    await waitFor(() => expect(patchSettingValues).toHaveBeenCalledWith({ mission_review_token_ceiling: '0' }))
  })

  it('shows the default as a placeholder when unset', async () => {
    vi.mocked(getSettings).mockResolvedValue({ settings: {}, values: {} })
    renderTab()
    const input = (await screen.findByRole('spinbutton', { name: 'Review token ceiling' })) as HTMLInputElement
    expect(input.value).toBe('')
    expect(input.placeholder).toBe('1500000')
  })

  it('failed Save keeps the value and shows an alert with Retry', async () => {
    vi.mocked(getSettings).mockResolvedValue({
      settings: {},
      values: { mission_review_token_ceiling: '250000' },
    })
    vi.mocked(patchSettingValues).mockRejectedValueOnce(new Error('network down'))
    renderTab()

    const input = (await screen.findByRole('spinbutton', { name: 'Review token ceiling' })) as HTMLInputElement
    fireEvent.change(input, { target: { value: '0' } })
    const region = screen.getByRole('region', { name: 'Review token ceiling' })
    fireEvent.click(within(region).getByRole('button', { name: 'Save' }))

    const alert = await within(region).findByRole('alert')
    expect(alert).toHaveTextContent('network down')
    expect(input.value).toBe('0')

    vi.mocked(patchSettingValues).mockResolvedValueOnce(undefined)
    fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(patchSettingValues).toHaveBeenCalledWith({ mission_review_token_ceiling: '0' }))
    expect(within(region).queryByRole('alert')).toBeNull()
  })
})
