import { render } from '@testing-library/react'
import { StrictMode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { OnboardingProgress } from '../api/types'

vi.mock('../api/client', () => ({ getToken: vi.fn() }))

import { getToken } from '../api/client'
import { OnboardingContext } from './context'
import { onboardingState } from './testing'
import { useMarkVisited } from './useVisited'

function Probe() {
  useMarkVisited('memory')
  return null
}

function renderProbe(progress: OnboardingProgress, updateProgress = vi.fn().mockResolvedValue(undefined)) {
  render(
    <StrictMode>
      <OnboardingContext.Provider value={{ ...onboardingState(), progress, updateProgress }}>
        <Probe />
      </OnboardingContext.Provider>
    </StrictMode>,
  )
  return updateProgress
}

beforeEach(() => {
  vi.mocked(getToken).mockReturnValue('tok')
})

describe('useMarkVisited', () => {
  it('patches the key once', () => {
    const update = renderProbe({})
    expect(update).toHaveBeenCalledTimes(1)
    expect(update).toHaveBeenCalledWith({ visited: ['memory'] })
  })

  it('does nothing when already visited', () => {
    expect(renderProbe({ visited: ['memory'] })).not.toHaveBeenCalled()
  })

  it('does nothing without a token', () => {
    vi.mocked(getToken).mockReturnValue('')
    expect(renderProbe({})).not.toHaveBeenCalled()
  })
})
