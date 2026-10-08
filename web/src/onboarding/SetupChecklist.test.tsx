import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { Mission, OnboardingProgress, Readiness } from '../api/types'

vi.mock('../api/client', () => ({ createMission: vi.fn() }))
vi.mock('sonner', () => ({ toast: { error: vi.fn() } }))

import { toast } from 'sonner'
import { createMission } from '../api/client'
import { OnboardingContext } from './context'
import { SetupChecklist } from './SetupChecklist'
import { onboardingState, readyReadiness } from './testing'

const fresh: Readiness = { ...readyReadiness, chat_route: false, first_chat: false, first_mission: false }

function renderChecklist(
  readiness: Readiness | null,
  progress: OnboardingProgress = {},
  updateProgress = vi.fn().mockResolvedValue(undefined),
) {
  render(
    <MemoryRouter initialEntries={['/']}>
      <OnboardingContext.Provider value={{ ...onboardingState(readiness), progress, updateProgress }}>
        <Routes>
          <Route path="/" element={<SetupChecklist />} />
          <Route path="/missions/:id" element={<p>mission page</p>} />
        </Routes>
      </OnboardingContext.Provider>
    </MemoryRouter>,
  )
  return updateProgress
}

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe('SetupChecklist', () => {
  it('shows how many items are done', () => {
    renderChecklist(readyReadiness)
    expect(screen.getByText('Set up Timothy')).toBeInTheDocument()
    expect(screen.getByText('3 of 8 done')).toBeInTheDocument()
    expect(screen.getByRole('progressbar')).toBeInTheDocument()
  })

  it('links undone items and hides the link on done ones', () => {
    renderChecklist(readyReadiness)
    expect(screen.getByRole('link', { name: 'Open: Give Timothy knowledge' })).toHaveAttribute('href', '/knowledge')
    expect(screen.queryByRole('link', { name: 'Open: Add a model provider' })).not.toBeInTheDocument()
  })

  it('renders nothing while loading', () => {
    renderChecklist(null)
    expect(screen.queryByText('Set up Timothy')).not.toBeInTheDocument()
  })

  it('renders nothing when dismissed', () => {
    renderChecklist(readyReadiness, { checklist_dismissed: true })
    expect(screen.queryByText('Set up Timothy')).not.toBeInTheDocument()
  })

  it('offers Dismiss only once a provider works', () => {
    renderChecklist(fresh)
    expect(screen.queryByRole('button', { name: 'Dismiss' })).not.toBeInTheDocument()
  })

  it('Dismiss hides the checklist', () => {
    const update = renderChecklist(readyReadiness)
    fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }))
    expect(update).toHaveBeenCalledWith({ checklist_dismissed: true })
  })

  it.each([
    ['no chat model', { chat_route: false }],
    ['no sandbox', { sandbox: false }],
  ])('hides the sample mission with %s', (_, patch) => {
    renderChecklist({ ...readyReadiness, first_mission: false, ...patch })
    expect(screen.queryByRole('button', { name: 'Run a sample mission' })).not.toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Open: Run your first mission' })).toBeInTheDocument()
  })

  it('hides the sample mission once a mission ran', () => {
    renderChecklist(readyReadiness)
    expect(screen.queryByRole('button', { name: 'Run a sample mission' })).not.toBeInTheDocument()
  })

  it('starts the sample mission and opens it', async () => {
    vi.mocked(createMission).mockResolvedValue({ id: 'm42' } as Mission)
    renderChecklist({ ...readyReadiness, first_mission: false })
    const button = screen.getByRole('button', { name: 'Run a sample mission' })
    fireEvent.click(button)
    expect(button).toBeDisabled()
    expect(await screen.findByText('mission page')).toBeInTheDocument()
    const input = vi.mocked(createMission).mock.calls[0][0]
    expect(input.goal).toContain('Run tag: onboarding-')
    expect(input).toMatchObject({ kind: 'general', light: true })
  })

  it('shows a toast when the sample mission fails', async () => {
    vi.mocked(createMission).mockRejectedValue(new Error('sandbox down'))
    renderChecklist({ ...readyReadiness, first_mission: false })
    const button = screen.getByRole('button', { name: 'Run a sample mission' })
    fireEvent.click(button)
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Could not start the sample mission', { description: 'sandbox down' }),
    )
    expect(button).toBeEnabled()
  })
})
