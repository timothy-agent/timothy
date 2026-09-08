import { cloneElement, isValidElement, useId, type FormHTMLAttributes, type ReactElement, type ReactNode } from 'react'
import { CircleAlert } from 'lucide-react'

import { cn } from '@/lib/utils'
import { Label } from '@/components/ui/label'

interface ControlProps {
  id: string
  'aria-describedby'?: string
  'aria-invalid'?: boolean
  'aria-required'?: boolean
}

// Drops undefined keys so cloneElement doesn't overwrite an explicit
// child prop (e.g. aria-invalid) with an absent Field value.
function definedControlProps(props: ControlProps): Partial<ControlProps> {
  return Object.fromEntries(Object.entries(props).filter(([, value]) => value !== undefined)) as Partial<ControlProps>
}

interface FieldProps {
  label: string
  description?: string
  error?: string
  required?: boolean
  optional?: boolean
  htmlFor?: string
  children: ((props: ControlProps) => ReactNode) | ReactElement
}

// The one form field wrapper: label, description, control, error, in
// that order (section 10.1). The control is passed either as a render
// function receiving the wiring props, or as a single element that
// gets those props cloned in.
export function Field({ label, description, error, required = true, optional, htmlFor, children }: FieldProps) {
  const generatedId = useId()
  const id = htmlFor ?? generatedId
  const descId = description ? `${id}-description` : undefined
  const errId = error ? `${id}-error` : undefined
  const describedBy = [descId, errId].filter(Boolean).join(' ') || undefined

  const controlProps: ControlProps = {
    id,
    'aria-describedby': describedBy,
    'aria-invalid': error ? true : undefined,
    'aria-required': required && !optional ? true : undefined,
  }

  return (
    <div className="space-y-2">
      <Label htmlFor={id}>
        {label}
        {optional && <span className="font-normal text-muted-foreground"> optional</span>}
      </Label>
      <div className="space-y-1.5">
        {description && (
          <p id={descId} className="text-sm text-muted-foreground">
            {description}
          </p>
        )}
        {typeof children === 'function'
          ? children(controlProps)
          : isValidElement(children)
            ? cloneElement(children, definedControlProps(controlProps))
            : children}
        {error && (
          <p id={errId} role="alert" className="flex items-center gap-1.5 text-xs text-destructive">
            <CircleAlert className="size-3" aria-hidden />
            {error}
          </p>
        )}
      </div>
    </div>
  )
}

interface FieldGroupProps {
  title?: string
  description?: string
  children: ReactNode
}

// Groups related fields under a Section title (section 10.3).
export function FieldGroup({ title, description, children }: FieldGroupProps) {
  return (
    <fieldset className="space-y-5">
      {title && <legend className="mb-1 text-section font-semibold">{title}</legend>}
      {description && <p className="mb-4 text-sm text-muted-foreground">{description}</p>}
      {children}
    </fieldset>
  )
}

// Form wrapper that stops native validation bubbles (section 10.5:
// the submit button stays enabled; disabled submits hide what is
// wrong, so validation is handled by the Field components instead).
export function Form({ className, children, ...props }: FormHTMLAttributes<HTMLFormElement>) {
  return (
    <form noValidate className={cn('space-y-8', className)} {...props}>
      {children}
    </form>
  )
}

interface FormActionsProps {
  children: ReactNode
  destructive?: ReactNode
  note?: ReactNode
  sticky?: boolean
  className?: string
}

// Right-aligned action row: destructive action far left, then the
// rest in order, primary action rightmost (section 10.4). note (e.g.
// "Unsaved changes") renders left of the buttons, 12px muted.
export function FormActions({ children, destructive, note, sticky, className }: FormActionsProps) {
  return (
    <div
      className={cn(
        'mt-8 flex items-center justify-end gap-2',
        sticky && 'sticky bottom-0 border-t border-border bg-background pt-4',
        className,
      )}
    >
      {destructive && <div className="mr-auto">{destructive}</div>}
      {note && <span className={cn('text-xs text-muted-foreground', !destructive && 'mr-auto')}>{note}</span>}
      {children}
    </div>
  )
}
