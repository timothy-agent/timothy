import { cleanup, render } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { providerPresets } from '../../lib/providerPresets'
import { LogoSprite, ProviderLogo } from './provider-logo'

afterEach(cleanup)

describe('ProviderLogo', () => {
  it.each(['deepseek', 'nebius'])('renders the %s mark from the sprite', (id) => {
    const preset = providerPresets.find((p) => p.id === id)!
    const { container } = render(
      <>
        <LogoSprite />
        <ProviderLogo preset={preset} />
      </>,
    )
    expect(container.querySelector('svg use')).toHaveAttribute('href', `#plogo-${id}`)
    expect(container.querySelector(`symbol#plogo-${id} path`)).toBeInTheDocument()
  })
})
