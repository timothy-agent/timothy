import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { describe, expect, it } from 'vitest'
import type { OnboardingProgress, Readiness } from '../api/types'
import { SidebarMenu, SidebarProvider } from '../components/ui/sidebar'
import { TooltipProvider } from '../components/ui/tooltip'
import { OnboardingContext } from './context'
import { SetupRing } from './SetupRing'
import { onboardingState, readyReadiness } from './testing'

function renderRing(readiness: Readiness | null, progress: OnboardingProgress = {}) {
  return render(
    <MemoryRouter>
      <TooltipProvider>
        <SidebarProvider>
          <OnboardingContext.Provider value={{ ...onboardingState(readiness), progress }}>
            <SidebarMenu>
              <SetupRing />
            </SidebarMenu>
          </OnboardingContext.Provider>
        </SidebarProvider>
      </TooltipProvider>
    </MemoryRouter>,
  )
}

describe('SetupRing', () => {
  it('shows progress and links home', () => {
    renderRing(readyReadiness)
    const link = screen.getByRole('link', { name: 'Setup 3/8' })
    expect(link).toHaveAttribute('href', '/')
  })

  it('is hidden while loading', () => {
    renderRing(null)
    expect(screen.queryByText(/Setup/)).not.toBeInTheDocument()
  })

  it('is hidden when dismissed', () => {
    renderRing(readyReadiness, { checklist_dismissed: true })
    expect(screen.queryByText(/Setup/)).not.toBeInTheDocument()
  })

  it('is hidden when every item is done', () => {
    renderRing(
      { ...readyReadiness, connectors: 1, kb_collections: 1 },
      { visited: ['memory', 'automations', 'analytics'] },
    )
    expect(screen.queryByText(/Setup/)).not.toBeInTheDocument()
  })
})
