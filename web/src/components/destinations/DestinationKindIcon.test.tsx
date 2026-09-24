import { cleanup, render } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { DestinationKindIcon } from './DestinationKindIcon'

afterEach(cleanup)

// lucide stamps a `lucide-<kebab-name>` class on every glyph it
// renders, so asserting on it pins which icon a kind maps to.
describe('DestinationKindIcon', () => {
  it('renders the mail glyph for an email destination', () => {
    const { container } = render(<DestinationKindIcon kind="email" />)
    expect(container.querySelector('.lucide-mail')).toBeInTheDocument()
  })

  it('renders the globe glyph for a webhook destination', () => {
    const { container } = render(<DestinationKindIcon kind="webhook" />)
    expect(container.querySelector('.lucide-globe')).toBeInTheDocument()
  })

  it('renders the message glyph for a channel destination', () => {
    const { container } = render(<DestinationKindIcon kind="channel" />)
    expect(container.querySelector('.lucide-message-square')).toBeInTheDocument()
  })

  it('references the github sprite symbol for a github destination', () => {
    const { container } = render(<DestinationKindIcon kind="github" />)
    expect(container.querySelector('use')).toHaveAttribute('href', '#clogo-github')
  })

  it('references the bitbucket sprite symbol for a bitbucket destination', () => {
    const { container } = render(<DestinationKindIcon kind="bitbucket" />)
    expect(container.querySelector('use')).toHaveAttribute('href', '#clogo-bitbucket')
  })

  it('references the gitlab sprite symbol for a gitlab destination', () => {
    const { container } = render(<DestinationKindIcon kind="gitlab" />)
    expect(container.querySelector('use')).toHaveAttribute('href', '#clogo-gitlab')
  })

  it('passes the size class through to the rendered glyph', () => {
    const { container } = render(<DestinationKindIcon kind="email" className="size-8" />)
    expect(container.querySelector('.lucide-mail')).toHaveClass('size-8')
  })
})
