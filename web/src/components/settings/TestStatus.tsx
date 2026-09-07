import type { ReactNode } from 'react'
import { Loader2 } from 'lucide-react'

import { Alert, AlertDescription } from '../ui/alert'
import { ProbeErrorDetails } from './ProbeErrorDetails'

interface TestStatusProps {
  state: 'idle' | 'testing' | 'ok' | 'failed'
  message?: string
  detail?: string
  action?: ReactNode
}

// TestStatus renders a Test/probe result as an Alert (contract 14.12):
// nothing while idle, a spinner while testing, success with the
// identity/latency line, failure with the 14.13 shape and an optional
// Retry/Reconnect action.
export function TestStatus({ state, message, detail, action }: TestStatusProps) {
  if (state === 'idle') return null

  if (state === 'testing') {
    return (
      <Alert tone="info" icon={<Loader2 className="size-4 animate-spin motion-keep text-info" aria-hidden />}>
        <AlertDescription>{message ?? 'Testing…'}</AlertDescription>
      </Alert>
    )
  }

  if (state === 'ok') {
    return (
      <Alert tone="good">
        <AlertDescription className="flex flex-wrap items-center justify-between gap-3">
          <span>{message}</span>
          {action}
        </AlertDescription>
      </Alert>
    )
  }

  return (
    <Alert tone="destructive">
      <AlertDescription className="space-y-2">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <ProbeErrorDetails summary={message ?? 'Test failed.'} raw={detail} />
          {action}
        </div>
      </AlertDescription>
    </Alert>
  )
}
