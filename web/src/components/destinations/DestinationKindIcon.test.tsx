import { cleanup, render } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { DestinationKindIcon } from './DestinationKindIcon'

afterEach(cleanup)

describe('DestinationKindIcon', () => {
  it('renders a Gmail image tile for an email destination', () => {
    const { container } = render(<DestinationKindIcon kind="email" />)
    expect(container.querySelector('img')).toBeInTheDocument()
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

  it('passes the size class through to the brand tile', () => {
    const { container } = render(<DestinationKindIcon kind="email" className="size-8" />)
    expect(container.firstElementChild).toHaveClass('size-8')
  })
})
