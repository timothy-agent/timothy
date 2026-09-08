import { cleanup, render, screen, fireEvent, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { RadioGroup, RadioGroupItem } from './radio-group'

afterEach(cleanup)

describe('RadioGroup', () => {
  it('moves selection with arrow keys', async () => {
    render(
      <RadioGroup defaultValue="a">
        <RadioGroupItem value="a" aria-label="A" />
        <RadioGroupItem value="b" aria-label="B" />
      </RadioGroup>,
    )
    const [a, b] = screen.getAllByRole('radio')
    expect(a).toHaveAttribute('aria-checked', 'true')
    a.focus()
    fireEvent.keyDown(a, { key: 'ArrowDown' })
    await waitFor(() => expect(b).toHaveAttribute('aria-checked', 'true'))
    expect(a).toHaveAttribute('aria-checked', 'false')
  })
})
