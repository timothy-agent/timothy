import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { getOnboarding, getSettings, getToken, listRoutes, patchOnboarding } from '../api/client'
import { OnboardingProvider } from '../onboarding/OnboardingProvider'
import { readyReadiness } from '../onboarding/testing'
import { shouldRedirectToWelcome } from '../onboarding/wizard/wizardState'
import { Welcome } from './Welcome'

const navigate = vi.fn()
vi.mock('react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('react-router')>()),
  useNavigate: () => navigate,
}))

vi.mock('../api/client', () => ({
  getOnboarding: vi.fn(),
  patchOnboarding: vi.fn(),
  getToken: vi.fn(),
  subscribeTokenChanged: vi.fn(() => () => {}),
  errorText: (err: unknown) => String(err),
  listRoutes: vi.fn(),
  getSettings: vi.fn(),
  patchSettingValues: vi.fn(),
}))

const noChat = { ...readyReadiness, chat_route: false }

function renderWelcome() {
  return render(
    <MemoryRouter initialEntries={['/welcome']}>
      <OnboardingProvider>
        <Welcome />
      </OnboardingProvider>
    </MemoryRouter>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(getToken).mockReturnValue('tok')
  vi.mocked(getOnboarding).mockResolvedValue({ readiness: noChat, progress: {} })
  vi.mocked(listRoutes).mockResolvedValue([])
  vi.mocked(getSettings).mockResolvedValue({ settings: {}, values: {} })
})

describe('shouldRedirectToWelcome', () => {
  it.each([
    ['no chat model, wizard not finished', noChat, {}, true],
    ['no chat model, wizard skipped', noChat, { wizard: 'skipped' as const }, false],
    ['no chat model, wizard done', noChat, { wizard: 'done' as const }, false],
    ['chat model, wizard not finished', readyReadiness, {}, false],
  ])('%s', (_, readiness, progress, want) => {
    expect(shouldRedirectToWelcome(readiness, progress, '/')).toBe(want)
  })

  it('never redirects while readiness is loading', () => {
    expect(shouldRedirectToWelcome(null, {}, '/')).toBe(false)
  })

  it('never redirects from a deep link', () => {
    expect(shouldRedirectToWelcome(noChat, {}, '/settings/providers')).toBe(false)
  })
})

describe('Welcome', () => {
  it('starts at the welcome step without a chat model', async () => {
    renderWelcome()
    expect(await screen.findByRole('heading', { name: 'Meet Timothy' })).toBeInTheDocument()
    expect(screen.getByText('Step 1 of 7')).toBeInTheDocument()
  })

  it('lists the sources with Ollama first under its own title', async () => {
    renderWelcome()
    fireEvent.click(await screen.findByRole('button', { name: "Let's start" }))
    const tiles = screen
      .getAllByRole('button')
      .map((b) => b.textContent ?? '')
      .filter((t) => t !== 'Skip for now')
    const titles = [
      'Local model (Ollama)',
      'OpenAI',
      'Anthropic',
      'GLM (Z.ai)',
      'Grok (xAI)',
      'AWS Bedrock',
      'Cursor',
      'Custom endpoint',
    ]
    expect(tiles).toHaveLength(titles.length)
    titles.forEach((title, i) => expect(tiles[i]).toContain(title))
    expect(tiles[0]).toContain('Runs on your own machine, no key needed')
    expect(screen.getByText('You can add more providers later in Settings.')).toBeInTheDocument()
  })

  it('starts at the model summary when chat already works', async () => {
    vi.mocked(getOnboarding).mockResolvedValue({ readiness: readyReadiness, progress: {} })
    renderWelcome()
    expect(await screen.findByRole('heading', { name: 'What Timothy uses' })).toBeInTheDocument()
    expect(screen.getByText('Step 5 of 7')).toBeInTheDocument()
  })

  it('a hello chip finishes the wizard and sends the message to chat', async () => {
    vi.mocked(getOnboarding).mockResolvedValue({ readiness: readyReadiness, progress: {} })
    vi.mocked(patchOnboarding).mockResolvedValue({ wizard: 'done' })
    renderWelcome()
    fireEvent.click(await screen.findByRole('button', { name: 'Continue' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Skip this step' }))
    fireEvent.click(await screen.findByRole('button', { name: 'What can you do for me?' }))
    await waitFor(() =>
      expect(navigate).toHaveBeenCalledWith('/chat', { state: { send: 'What can you do for me?' } }),
    )
    expect(patchOnboarding).toHaveBeenCalledWith({ wizard: 'done' })
  })

  it('exploring alone finishes the wizard and goes home', async () => {
    vi.mocked(getOnboarding).mockResolvedValue({ readiness: readyReadiness, progress: {} })
    vi.mocked(patchOnboarding).mockResolvedValue({ wizard: 'done' })
    renderWelcome()
    fireEvent.click(await screen.findByRole('button', { name: 'Continue' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Skip this step' }))
    fireEvent.click(await screen.findByRole('button', { name: "I'll explore on my own" }))
    await waitFor(() => expect(navigate).toHaveBeenCalledWith('/', undefined))
    expect(patchOnboarding).toHaveBeenCalledWith({ wizard: 'done' })
  })

  it('skip stores the choice and goes home', async () => {
    vi.mocked(patchOnboarding).mockResolvedValue({ wizard: 'skipped' })
    renderWelcome()
    await screen.findByRole('heading', { name: 'Meet Timothy' })
    fireEvent.click(screen.getAllByRole('button', { name: 'Skip for now' })[0])
    await waitFor(() => expect(navigate).toHaveBeenCalledWith('/'))
    expect(patchOnboarding).toHaveBeenCalledWith({ wizard: 'skipped' })
  })
})
