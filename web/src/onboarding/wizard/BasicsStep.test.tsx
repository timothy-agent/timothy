import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { getSettings, patchSettingValues } from '../../api/client'
import { BasicsStep } from './BasicsStep'

vi.mock('../../api/client', () => ({ getSettings: vi.fn(), patchSettingValues: vi.fn() }))

const browserZone = Intl.DateTimeFormat().resolvedOptions().timeZone

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(patchSettingValues).mockResolvedValue()
})

describe('BasicsStep', () => {
  it('prefills the browser timezone when none is stored and saves both values', async () => {
    vi.mocked(getSettings).mockResolvedValue({ settings: {}, values: {} })
    const onNext = vi.fn()
    render(<BasicsStep onBack={() => {}} onNext={onNext} />)
    expect(await screen.findByRole('combobox', { name: 'Timezone' })).toHaveTextContent(browserZone)
    fireEvent.click(screen.getByRole('button', { name: 'Save and continue' }))
    await waitFor(() => expect(onNext).toHaveBeenCalled())
    expect(patchSettingValues).toHaveBeenCalledWith({ timezone: browserZone, default_currency: 'USD' })
  })

  it('keeps the stored values', async () => {
    vi.mocked(getSettings).mockResolvedValue({ settings: {}, values: { timezone: 'Asia/Dhaka', default_currency: 'EUR' } })
    render(<BasicsStep onBack={() => {}} onNext={() => {}} />)
    await waitFor(() => expect(screen.getByRole('combobox', { name: 'Timezone' })).toHaveTextContent('Asia/Dhaka'))
    fireEvent.click(screen.getByRole('button', { name: 'Save and continue' }))
    await waitFor(() =>
      expect(patchSettingValues).toHaveBeenCalledWith({ timezone: 'Asia/Dhaka', default_currency: 'EUR' }),
    )
  })

  it('skip moves on without saving', async () => {
    vi.mocked(getSettings).mockResolvedValue({ settings: {}, values: {} })
    const onNext = vi.fn()
    render(<BasicsStep onBack={() => {}} onNext={onNext} />)
    fireEvent.click(screen.getByRole('button', { name: 'Skip this step' }))
    expect(onNext).toHaveBeenCalled()
    expect(patchSettingValues).not.toHaveBeenCalled()
  })
})
