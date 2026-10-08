import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { describe, expect, it } from 'vitest'
import type { Readiness, ReadinessKey } from '../api/types'
import { OnboardingContext } from './context'
import { SetupGate } from './SetupGate'
import { onboardingState, readyReadiness } from './testing'

function renderGate(readiness: Readiness | null, requires: ReadinessKey[], variant: 'panel' | 'banner') {
  return render(
    <MemoryRouter>
      <OnboardingContext.Provider value={onboardingState(readiness)}>
        <SetupGate requires={requires} variant={variant}>
          <p>gated content</p>
        </SetupGate>
      </OnboardingContext.Provider>
    </MemoryRouter>,
  )
}

describe('SetupGate', () => {
  it('renders children when every key is met', () => {
    renderGate(readyReadiness, ['chat_route', 'sandbox'], 'panel')
    expect(screen.getByText('gated content')).toBeInTheDocument()
  })

  it('renders children while readiness is still loading', () => {
    renderGate(null, ['chat_route'], 'panel')
    expect(screen.getByText('gated content')).toBeInTheDocument()
  })

  it.each([
    ['chat_route', 'Timothy has no model for chat yet', 'Add a provider', '/settings/providers'],
    ['embedding_route', 'Knowledge needs an embedding model', 'Add a provider', '/settings/providers'],
    ['vision_route', 'No model can look at images yet', 'Add a provider', '/settings/providers'],
    ['automations_enabled', 'Automations are switched off', 'Open Features', '/settings/features'],
  ] as const)('panel for unmet %s shows its copy and action', (key, title, label, href) => {
    renderGate({ ...readyReadiness, [key]: false }, [key], 'panel')
    expect(screen.queryByText('gated content')).not.toBeInTheDocument()
    expect(screen.getByText(title)).toBeInTheDocument()
    expect(screen.getByRole('link', { name: label })).toHaveAttribute('href', href)
  })

  it('panel for an unmet sandbox has no action', () => {
    renderGate({ ...readyReadiness, sandbox: false }, ['sandbox'], 'panel')
    expect(screen.getByText('Missions need the sandbox service')).toBeInTheDocument()
    expect(screen.queryByRole('link')).not.toBeInTheDocument()
  })

  it('a down gateway wins over an unmet model key', () => {
    renderGate({ ...readyReadiness, gateway_ready: false, chat_route: false }, ['chat_route'], 'panel')
    expect(screen.getByText("Timothy can't reach its model gateway")).toBeInTheDocument()
    expect(screen.queryByText('Timothy has no model for chat yet')).not.toBeInTheDocument()
  })

  it('a down gateway does not block a gate that needs no model', () => {
    renderGate({ ...readyReadiness, gateway_ready: false }, ['automations_enabled'], 'panel')
    expect(screen.getByText('gated content')).toBeInTheDocument()
  })

  it('explains the first unmet key in requested order', () => {
    renderGate({ ...readyReadiness, sandbox: false, automations_enabled: false }, ['automations_enabled', 'sandbox'], 'panel')
    expect(screen.getByText('Automations are switched off')).toBeInTheDocument()
  })

  it('banner renders the alert above its children', () => {
    renderGate({ ...readyReadiness, chat_route: false }, ['chat_route'], 'banner')
    expect(screen.getByRole('status')).toHaveTextContent('Timothy has no model for chat yet')
    expect(screen.getByText('gated content')).toBeInTheDocument()
  })
})
