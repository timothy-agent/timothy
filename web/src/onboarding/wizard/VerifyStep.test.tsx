import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { useReducer } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { testProvider } from '../../api/client'
import { VerifyStep } from './VerifyStep'
import { initialWizardState, wizardReducer } from './wizardState'

vi.mock('../../api/client', () => ({ testProvider: vi.fn() }))

// Harness wires the step to the real reducer, as Welcome does.
function Harness({ onNext, onBack }: { onNext: () => void; onBack: () => void }) {
  const [state, dispatch] = useReducer(wizardReducer, {
    ...initialWizardState(false),
    step: 'verify' as const,
    provider: { id: 'p1', name: 'Ollama' },
  })
  return (
    <VerifyStep
      provider={state.provider!}
      verified={state.verified}
      verifyError={state.verifyError}
      onVerified={(result) => dispatch({ type: 'verified', result })}
      onFailed={(detail) => dispatch({ type: 'verifyFailed', detail })}
      onRetry={() => dispatch({ type: 'providerCreated', id: 'p1', name: 'Ollama' })}
      onBack={onBack}
      onNext={onNext}
    />
  )
}

beforeEach(() => vi.clearAllMocks())

describe('VerifyStep', () => {
  it('shows the latency and moves on after a passing check', async () => {
    vi.mocked(testProvider).mockResolvedValue({ ok: true, latency_ms: 42, model: 'qwen2.5:7b' })
    const onNext = vi.fn()
    render(<Harness onNext={onNext} onBack={() => {}} />)
    expect(screen.getByText('Checking the connection...')).toBeInTheDocument()
    expect(await screen.findByText('Connected, 42 ms')).toBeInTheDocument()
    await waitFor(() => expect(onNext).toHaveBeenCalled(), { timeout: 3000 })
    expect(testProvider).toHaveBeenCalledTimes(1)
    expect(testProvider).toHaveBeenCalledWith('p1')
  })

  it('shows the exact detail on failure and retries', async () => {
    vi.mocked(testProvider).mockResolvedValueOnce({ ok: false, latency_ms: 0, model: 'm', detail: 'dial tcp: connection refused' })
    const onBack = vi.fn()
    render(<Harness onNext={() => {}} onBack={onBack} />)
    expect(await screen.findByText('dial tcp: connection refused')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Back' }))
    expect(onBack).toHaveBeenCalled()

    vi.mocked(testProvider).mockResolvedValueOnce({ ok: true, latency_ms: 7, model: 'm' })
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('Connected, 7 ms')).toBeInTheDocument()
    expect(testProvider).toHaveBeenCalledTimes(2)
  })
})
