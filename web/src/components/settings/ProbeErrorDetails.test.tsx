import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { ProbeErrorDetails } from './ProbeErrorDetails'

afterEach(cleanup)

describe('ProbeErrorDetails', () => {
  it('renders summary alone when no cause, hint or raw is given', () => {
    render(<ProbeErrorDetails summary="Test failed." />)
    expect(screen.getByText('Test failed.')).toBeInTheDocument()
    expect(screen.queryByText('Details')).not.toBeInTheDocument()
  })

  it('renders cause inline with the summary', () => {
    render(<ProbeErrorDetails summary="Test failed." cause="401 unauthorized" />)
    expect(screen.getByText('401 unauthorized')).toBeInTheDocument()
  })

  it('renders a hint line below the summary', () => {
    render(<ProbeErrorDetails summary="Test failed." hint="Check the API key." />)
    expect(screen.getByText('Check the API key.')).toBeInTheDocument()
  })

  it('hides the raw payload behind a Details disclosure', () => {
    render(<ProbeErrorDetails summary="Test failed." raw="full stack trace" />)
    expect(screen.queryByText('full stack trace')).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: /Details/ }))
    expect(screen.getByText('full stack trace')).toBeInTheDocument()
  })
})
