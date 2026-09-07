import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { Kbd, KbdGroup } from './kbd'

afterEach(cleanup)

describe('Kbd', () => {
  it('renders a kbd element', () => {
    render(<Kbd>S</Kbd>)
    const el = screen.getByText('S')
    expect(el.tagName).toBe('KBD')
  })

  it('renders a group of kbd elements', () => {
    render(
      <KbdGroup>
        <Kbd>Ctrl</Kbd>
        <Kbd>S</Kbd>
      </KbdGroup>,
    )
    expect(screen.getByText('Ctrl').tagName).toBe('KBD')
    expect(screen.getByText('S').tagName).toBe('KBD')
  })
})
