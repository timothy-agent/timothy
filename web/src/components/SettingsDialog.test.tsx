import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { setToken } from '../api/client'
import { SettingsDialog } from './SettingsDialog'

afterEach(cleanup)

describe('SettingsDialog', () => {
  it('discards unsaved edits when reopened', () => {
    setToken('saved-token')
    const { rerender } = render(<SettingsDialog open onClose={() => {}} />)

    const input = screen.getByLabelText('API token')
    fireEvent.change(input, { target: { value: 'half-typed' } })
    expect(input).toHaveValue('half-typed')

    rerender(<SettingsDialog open={false} onClose={() => {}} />)
    rerender(<SettingsDialog open onClose={() => {}} />)

    expect(screen.getByLabelText('API token')).toHaveValue('saved-token')
  })
})
