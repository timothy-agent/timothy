import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { ModelBadge } from './ModelBadge'

afterEach(cleanup)

describe('ModelBadge', () => {
  it('renders the model name with the provider brand mark for a recognized provider', () => {
    render(<ModelBadge provider="zai-glm" model="glm-4.7" />)
    expect(screen.getByText('glm-4.7')).toBeInTheDocument()
    expect(screen.getByText('glm-4.7').closest('span')?.querySelector('svg use')).toHaveAttribute(
      'href',
      '#plogo-zai',
    )
  })

  it('omits the mark for an unrecognized provider name', () => {
    render(<ModelBadge provider="my-custom-endpoint" model="whatever" />)
    expect(screen.getByText('whatever')).toBeInTheDocument()
    expect(screen.getByText('whatever').closest('span')?.querySelector('svg use')).not.toBeInTheDocument()
  })

  it('renders extra children after the model name', () => {
    render(
      <ModelBadge provider="zai-glm" model="glm-4.7">
        <span>11→204 tok</span>
      </ModelBadge>,
    )
    expect(screen.getByText('11→204 tok')).toBeInTheDocument()
  })

  it('applies an optional title', () => {
    render(<ModelBadge provider="zai-glm" model="glm-4.7" title="7 calls via zai-glm" />)
    expect(screen.getByText('glm-4.7').closest('span')).toHaveAttribute('title', '7 calls via zai-glm')
  })
})
