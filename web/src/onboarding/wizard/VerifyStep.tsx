import { Loader2 } from 'lucide-react'
import { useEffect } from 'react'
import { testProvider } from '../../api/client'
import type { TestResult } from '../../api/types'
import { Button } from '../../components/ui/button'
import { errText } from '../../lib/errors'

interface VerifyStepProps {
  provider: { id: string; name: string }
  verified: TestResult | null
  verifyError: string | null
  onVerified: (result: TestResult) => void
  onFailed: (detail: string) => void
  onRetry: () => void
  onBack: () => void
  onNext: () => void
}

// How long "Connected" stays on screen before the wizard moves on.
const advanceDelayMs = 1200

export function VerifyStep({ provider, verified, verifyError, onVerified, onFailed, onRetry, onBack, onNext }: VerifyStepProps) {
  const checking = verified === null && verifyError === null

  useEffect(() => {
    if (!checking) return
    let active = true
    testProvider(provider.id).then(
      (res) => {
        if (!active) return
        if (res.ok) onVerified(res)
        else onFailed(res.detail ?? 'The provider did not answer.')
      },
      (err: unknown) => {
        if (active) onFailed(errText(err))
      },
    )
    return () => {
      active = false
    }
  }, [checking, provider.id, onVerified, onFailed])

  useEffect(() => {
    if (!verified) return
    const t = setTimeout(onNext, advanceDelayMs)
    return () => clearTimeout(t)
  }, [verified, onNext])

  return (
    <div>
      <h1 className="text-xl font-semibold">Checking {provider.name}</h1>
      <div className="mt-6" role="status">
        {checking && (
          <p className="flex items-center gap-2 text-sm text-muted-foreground">
            <Loader2 className="size-4 animate-spin" aria-hidden />
            Checking the connection...
          </p>
        )}
        {verified && <p className="text-sm font-medium text-good">Connected, {verified.latency_ms} ms</p>}
        {verifyError !== null && <p className="text-sm text-destructive">{verifyError}</p>}
      </div>
      {verifyError !== null && (
        <div className="mt-6 flex gap-2">
          <Button variant="outline" onClick={onBack}>
            Back
          </Button>
          <Button onClick={onRetry}>Retry</Button>
        </div>
      )}
    </div>
  )
}
