import { useCallback, useRef, useState, type ReactNode } from 'react'
import { Loader2 } from 'lucide-react'

import { Button } from '@/components/ui/button'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'

interface ConfirmDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  description: string
  confirmLabel?: string
  cancelLabel?: string
  destructive?: boolean
  loading?: boolean
  onConfirm: () => void
  children?: ReactNode
}

// One confirm dialog for every "are you sure" prompt. Negative action
// stays neutral (outline Cancel); destructive prompts get the
// destructive variant on Confirm (section 0.7).
export function ConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmLabel = 'Confirm',
  cancelLabel = 'Cancel',
  destructive = false,
  loading = false,
  onConfirm,
  children,
}: ConfirmDialogProps) {
  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>{description}</AlertDialogDescription>
        </AlertDialogHeader>
        {children}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={loading} asChild>
            <Button variant="outline" disabled={loading}>
              {cancelLabel}
            </Button>
          </AlertDialogCancel>
          <AlertDialogAction
            onClick={(event) => {
              event.preventDefault()
              onConfirm()
            }}
            disabled={loading}
            aria-busy={loading}
            data-variant={destructive ? 'destructive' : 'default'}
            asChild
          >
            <Button variant={destructive ? 'destructive' : 'default'} disabled={loading} aria-busy={loading}>
              {loading && <Loader2 className="size-4 animate-spin motion-keep" aria-hidden />}
              {confirmLabel}
            </Button>
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

interface ConfirmOptions {
  title: string
  description: string
  confirmLabel?: string
  cancelLabel?: string
  destructive?: boolean
}

// Imperative helper for the common case: await confirm(opts) resolves
// true/false and renders the dialog element wherever it is placed.
export function useConfirm(): [(opts: ConfirmOptions) => Promise<boolean>, ReactNode] {
  const [open, setOpen] = useState(false)
  const [options, setOptions] = useState<ConfirmOptions | null>(null)
  const resolver = useRef<(value: boolean) => void>(undefined)

  const confirm = useCallback((opts: ConfirmOptions) => {
    setOptions(opts)
    setOpen(true)
    return new Promise<boolean>((resolve) => {
      resolver.current = resolve
    })
  }, [])

  const settle = useCallback((value: boolean) => {
    setOpen(false)
    resolver.current?.(value)
    resolver.current = undefined
  }, [])

  const element = options && (
    <ConfirmDialog
      open={open}
      onOpenChange={(next) => {
        if (!next) settle(false)
      }}
      title={options.title}
      description={options.description}
      confirmLabel={options.confirmLabel}
      cancelLabel={options.cancelLabel}
      destructive={options.destructive}
      onConfirm={() => settle(true)}
    />
  )

  return [confirm, element]
}
