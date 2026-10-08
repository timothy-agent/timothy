import { Wrench } from 'lucide-react'
import { Link } from 'react-router'
import type { ReadinessKey } from '../api/types'
import { EmptyState } from '../components/timothy/empty-state'
import { Alert, AlertDescription, AlertTitle } from '../components/ui/alert'
import { Button } from '../components/ui/button'
import { useOnboarding } from './context'
import { gateCopy, unmetKey } from './gateCopy'

interface SetupGateProps {
  requires: ReadinessKey[]
  // panel replaces children; banner shows a notice above them and the
  // caller decides what to disable.
  variant: 'panel' | 'banner'
  children?: React.ReactNode
}

export function SetupGate({ requires, variant, children }: SetupGateProps) {
  const { readiness } = useOnboarding()
  const key = unmetKey(readiness, requires)
  const copy = key ? gateCopy[key] : undefined
  if (!copy) return <>{children}</>

  const action = copy.action && (
    <Button asChild variant={variant === 'panel' ? 'default' : 'outline'} size="sm">
      <Link to={copy.action.to}>{copy.action.label}</Link>
    </Button>
  )
  if (variant === 'panel') {
    return <EmptyState icon={Wrench} title={copy.title} description={copy.description} action={action} />
  }
  return (
    <>
      <Alert tone="warning" className="mb-3">
        <AlertTitle>{copy.title}</AlertTitle>
        <AlertDescription className="flex flex-wrap items-center justify-between gap-2">
          <span>{copy.description}</span>
          {action}
        </AlertDescription>
      </Alert>
      {children}
    </>
  )
}
