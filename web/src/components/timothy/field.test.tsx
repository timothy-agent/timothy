import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Field, FieldGroup, Form, FormActions } from './field'
import { Input } from '@/components/ui/input'

describe('Field', () => {
  it('wires htmlFor, aria-describedby for description, and control id', () => {
    render(
      <Field label="Name" description="Your display name">
        {(props) => <Input {...props} />}
      </Field>,
    )
    const input = screen.getByRole('textbox')
    const label = screen.getByText('Name')
    expect(label.tagName).toBe('LABEL')
    expect(label).toHaveAttribute('for', input.id)
    expect(input).toHaveAttribute('aria-describedby', expect.stringContaining(`${input.id}-description`))
  })

  it('sets aria-invalid and renders the error with role=alert, linked via aria-describedby', () => {
    render(
      <Field label="Name" error="Name is required">
        {(props) => <Input {...props} />}
      </Field>,
    )
    const input = screen.getByRole('textbox')
    expect(input).toHaveAttribute('aria-invalid', 'true')
    const error = screen.getByRole('alert')
    expect(error).toHaveTextContent('Name is required')
    expect(input.getAttribute('aria-describedby')).toContain(`${input.id}-error`)
  })

  it('shows the optional suffix', () => {
    render(
      <Field label="Nickname" required={false} optional>
        {(props) => <Input {...props} />}
      </Field>,
    )
    expect(screen.getByText('optional')).toBeInTheDocument()
  })

  it('accepts a single cloned element as children', () => {
    render(
      <Field label="Email" htmlFor="email-field">
        <Input />
      </Field>,
    )
    const input = screen.getByRole('textbox')
    expect(input).toHaveAttribute('id', 'email-field')
    expect(screen.getByText('Email')).toHaveAttribute('for', 'email-field')
  })
})

describe('FieldGroup', () => {
  it('renders a fieldset with a legend', () => {
    render(
      <FieldGroup title="Account">
        <p>fields</p>
      </FieldGroup>,
    )
    const fieldset = screen.getByText('fields').closest('fieldset')
    expect(fieldset).toBeInTheDocument()
    expect(fieldset?.querySelector('legend')).toHaveTextContent('Account')
  })
})

describe('Form', () => {
  it('renders a form with novalidate', () => {
    render(
      <Form>
        <p>content</p>
      </Form>,
    )
    const form = screen.getByText('content').closest('form')
    expect(form).toHaveAttribute('novalidate')
  })
})

describe('FormActions', () => {
  it('renders destructive action mr-auto ahead of the rest', () => {
    render(
      <FormActions destructive={<button>Delete</button>}>
        <button>Cancel</button>
        <button>Save</button>
      </FormActions>,
    )
    expect(screen.getByRole('button', { name: 'Delete' }).parentElement).toHaveClass('mr-auto')
  })
})
