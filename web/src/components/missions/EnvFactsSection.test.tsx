import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import type { EnvFacts } from '../../api/types'
import { TooltipProvider } from '../ui/tooltip'
import { EnvFactsSection } from './EnvFactsSection'

afterEach(cleanup)

function renderExpanded(facts: EnvFacts) {
  render(
    <TooltipProvider>
      <EnvFactsSection facts={facts} />
    </TooltipProvider>,
  )
  fireEvent.click(screen.getByRole('button', { name: 'Show environment' }))
}

describe('EnvFactsSection', () => {
  it('starts collapsed', () => {
    render(
      <TooltipProvider>
        <EnvFactsSection facts={{ base_branch: 'main' }} />
      </TooltipProvider>,
    )
    expect(screen.queryByText('main')).not.toBeInTheDocument()
  })

  it('shows branch, delivery, manifests by directory, tools, absent tools and gaps', () => {
    renderExpanded({
      base_branch: 'main',
      destinations: [{ kind: 'github', mode: 'push_pr' }],
      manifests: ['package.json', 'composer.json', 'web/package.json'],
      tools: [{ name: 'docker' }, { name: 'git', version: 'git version 2.39.5' }, { name: 'gh' }],
      gaps: ['Testcontainers needs Docker.'],
    })
    expect(screen.getByText('main')).toBeInTheDocument()
    expect(screen.getByText('github (push_pr)')).toBeInTheDocument()
    expect(screen.getByText('.').closest('li')).toHaveTextContent('.: composer.json, package.json')
    expect(screen.getByText('web').closest('li')).toHaveTextContent('web: package.json')
    expect(screen.getByText('git version 2.39.5')).toBeInTheDocument()
    expect(screen.getByText('docker, gh')).toBeInTheDocument()
    expect(screen.getByText('Testcontainers needs Docker.')).toBeInTheDocument()
  })

  it('says when there is no repository destination', () => {
    renderExpanded({ manifests: ['go.mod'] })
    expect(screen.getByText('No repository destination')).toBeInTheDocument()
  })
})
