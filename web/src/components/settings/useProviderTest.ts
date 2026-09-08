import { useState } from 'react'
import { testProvider } from '../../api/client'
import { probeFailureText, responsesSuffix } from './util'
import { isTimothyAuthDetail, isTimothyAuthError } from '../../lib/errors'

interface ProviderTestState {
  state: 'idle' | 'testing' | 'ok' | 'failed'
  message?: string
  detail?: string
}

// useProviderTest wraps testProvider with the shared probe-result
// rendering rules: a Timothy auth failure never paints as a provider
// probe miss (it stays idle), success renders "OK, {model} answered in
// {ms} ms" plus the responses-API suffix, failure renders the humanized
// probe detail.
export function useProviderTest(id: string) {
  const [result, setResult] = useState<ProviderTestState>({ state: 'idle' })

  const run = async () => {
    setResult({ state: 'testing' })
    try {
      const res = await testProvider(id)
      if (!res.ok && isTimothyAuthDetail(res.detail)) {
        setResult({ state: 'idle' })
        return
      }
      if (res.ok) {
        setResult({ state: 'ok', message: `OK, ${res.model} answered in ${res.latency_ms} ms.${responsesSuffix(res)}` })
      } else {
        setResult({ state: 'failed', message: probeFailureText(res), detail: res.detail })
      }
    } catch (err) {
      if (isTimothyAuthError(err)) {
        setResult({ state: 'idle' })
        return
      }
      const detail = err instanceof Error ? err.message : String(err)
      setResult({ state: 'failed', message: probeFailureText({ latency_ms: 0, detail }), detail })
    }
  }

  return { ...result, run }
}
