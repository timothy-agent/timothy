import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { Link, MemoryRouter } from 'react-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { getOnboarding, getToken, patchOnboarding, subscribeTokenChanged } from '../api/client'
import { useOnboarding } from './context'
import { OnboardingProvider } from './OnboardingProvider'
import { readyReadiness } from './testing'

vi.mock('../api/client', () => ({
  getOnboarding: vi.fn(),
  patchOnboarding: vi.fn(),
  getToken: vi.fn(),
  subscribeTokenChanged: vi.fn(() => () => {}),
  errorText: (err: unknown) => String(err),
}))

function Probe() {
  const { readiness, progress, updateProgress } = useOnboarding()
  return (
    <div>
      <p>chat: {readiness === null ? 'loading' : String(readiness.chat_route)}</p>
      <p>wizard: {progress.wizard ?? 'none'}</p>
      <button onClick={() => void updateProgress({ wizard: 'done' })}>finish</button>
      <Link to="/missions">go</Link>
    </div>
  )
}

function renderProvider() {
  return render(
    <MemoryRouter initialEntries={['/']}>
      <OnboardingProvider>
        <Probe />
      </OnboardingProvider>
    </MemoryRouter>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(getToken).mockReturnValue('tok')
  vi.mocked(getOnboarding).mockResolvedValue({ readiness: readyReadiness, progress: {} })
})

describe('OnboardingProvider', () => {
  it('fetches on mount', async () => {
    renderProvider()
    expect(await screen.findByText('chat: true')).toBeInTheDocument()
    expect(getOnboarding).toHaveBeenCalledTimes(1)
  })

  it('refetches when the pathname changes', async () => {
    renderProvider()
    await screen.findByText('chat: true')
    fireEvent.click(screen.getByRole('link', { name: 'go' }))
    await waitFor(() => expect(getOnboarding).toHaveBeenCalledTimes(2))
  })

  it('fetches once a token is stored', async () => {
    vi.mocked(getToken).mockReturnValue('')
    renderProvider()
    expect(screen.getByText('chat: loading')).toBeInTheDocument()
    expect(getOnboarding).not.toHaveBeenCalled()
    vi.mocked(getToken).mockReturnValue('tok')
    const listener = vi.mocked(subscribeTokenChanged).mock.calls[0][0]
    act(() => listener())
    expect(await screen.findByText('chat: true')).toBeInTheDocument()
    expect(getOnboarding).toHaveBeenCalledTimes(1)
  })

  it('refetches on window focus', async () => {
    renderProvider()
    await screen.findByText('chat: true')
    act(() => {
      window.dispatchEvent(new Event('focus'))
    })
    await waitFor(() => expect(getOnboarding).toHaveBeenCalledTimes(2))
  })

  it('updateProgress patches and stores the merged result', async () => {
    vi.mocked(patchOnboarding).mockResolvedValue({ wizard: 'done' })
    renderProvider()
    await screen.findByText('chat: true')
    fireEvent.click(screen.getByRole('button', { name: 'finish' }))
    expect(await screen.findByText('wizard: done')).toBeInTheDocument()
    expect(patchOnboarding).toHaveBeenCalledWith({ wizard: 'done' })
  })

  it('does not fetch without a token', async () => {
    vi.mocked(getToken).mockReturnValue('')
    renderProvider()
    act(() => {
      window.dispatchEvent(new Event('focus'))
    })
    expect(screen.getByText('chat: loading')).toBeInTheDocument()
    expect(getOnboarding).not.toHaveBeenCalled()
  })

  it('useOnboarding throws outside the provider', () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    expect(() => render(<Probe />)).toThrow('useOnboarding must be used inside OnboardingProvider')
  })
})
