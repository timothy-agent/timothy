import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { Alert, AlertTitle, AlertDescription } from './alert'

afterEach(cleanup)

describe('Alert', () => {
  it('uses role=status and a good icon for tone=good', () => {
    render(
      <Alert tone="good">
        <AlertTitle>Done</AlertTitle>
        <AlertDescription>All good.</AlertDescription>
      </Alert>,
    )
    const el = screen.getByRole('status')
    expect(el.querySelector('svg')).toBeInTheDocument()
  })

  it('uses role=alert for tone=destructive', () => {
    render(
      <Alert tone="destructive">
        <AlertTitle>Failed</AlertTitle>
      </Alert>,
    )
    expect(screen.getByRole('alert')).toBeInTheDocument()
  })

  it('omits the icon when icon={null}', () => {
    render(
      <Alert tone="good" icon={null}>
        <AlertTitle>Done</AlertTitle>
      </Alert>,
    )
    expect(screen.getByRole('status').querySelector('svg')).not.toBeInTheDocument()
  })
})
