import { useCallback, useEffect, useReducer, useState } from 'react'
import { useNavigate } from 'react-router'
import { toast } from 'sonner'
import type { TestResult } from '../api/types'
import { BrandMark } from '../components/BrandMark'
import { ProviderAdd } from '../components/settings/ProviderAdd'
import { Button } from '../components/ui/button'
import { Progress } from '../components/ui/progress'
import { errText } from '../lib/errors'
import { providerPresets } from '../lib/providerPresets'
import { useOnboarding } from '../onboarding/context'
import { BasicsStep } from '../onboarding/wizard/BasicsStep'
import { HelloStep } from '../onboarding/wizard/HelloStep'
import { RolesStep } from '../onboarding/wizard/RolesStep'
import { SourceStep } from '../onboarding/wizard/SourceStep'
import { VerifyStep } from '../onboarding/wizard/VerifyStep'
import { initialWizardState, wizardReducer, wizardSteps } from '../onboarding/wizard/wizardState'
import type { ChatIntent } from './Home'

// Welcome is the first-run wizard: a full-screen page outside the app
// shell, so the provider form's popovers work and browser Back leaves.
export function Welcome() {
  const { readiness, loading } = useOnboarding()
  if (loading) return null
  return <Wizard chatReady={readiness?.chat_route ?? false} />
}

function Wizard({ chatReady }: { chatReady: boolean }) {
  const [state, dispatch] = useReducer(wizardReducer, chatReady, initialWizardState)
  const { updateProgress } = useOnboarding()
  const navigate = useNavigate()
  const [busy, setBusy] = useState(false)

  // A failed save still leaves: Home sends the operator back here.
  useEffect(() => {
    if (!state.skipped) return
    updateProgress({ wizard: 'skipped' })
      .catch((err: unknown) => toast.error('Could not save your choice', { description: errText(err) }))
      .finally(() => navigate('/'))
  }, [state.skipped, updateProgress, navigate])

  const finish = async (to: string, intent?: ChatIntent) => {
    setBusy(true)
    try {
      await updateProgress({ wizard: 'done' })
      navigate(to, intent ? { state: intent } : undefined)
    } catch (err) {
      toast.error('Could not save your progress', { description: errText(err) })
      setBusy(false)
    }
  }

  const next = useCallback(() => dispatch({ type: 'next' }), [])
  const back = useCallback(() => dispatch({ type: 'back' }), [])
  const onVerified = useCallback((result: TestResult) => dispatch({ type: 'verified', result }), [])
  const onFailed = useCallback((detail: string) => dispatch({ type: 'verifyFailed', detail }), [])

  const stepNumber = wizardSteps.indexOf(state.step) + 1
  const preset = providerPresets.find((p) => p.id === state.presetId)

  return (
    <div className="flex min-h-dvh flex-col bg-background">
      <header className="flex h-12 shrink-0 items-center gap-2 border-b border-border px-4">
        <BrandMark className="size-5" />
        <span className="font-mono text-sm font-semibold tracking-[0.2em] text-brand-text uppercase">Timothy</span>
        <Button
          variant="ghost"
          size="sm"
          className="ml-auto"
          disabled={state.skipped || busy}
          onClick={() => dispatch({ type: 'skip' })}
        >
          Skip for now
        </Button>
      </header>
      <main className="mx-auto w-full max-w-2xl flex-1 px-4 py-8 sm:py-12">
        <p className="text-xs text-muted-foreground">
          Step {stepNumber} of {wizardSteps.length}
        </p>
        <Progress
          className="mt-2"
          value={(stepNumber / wizardSteps.length) * 100}
          aria-label={`Step ${stepNumber} of ${wizardSteps.length}`}
        />
        <div className="mt-10">
          {state.step === 'welcome' && (
            <div>
              <h1 className="text-2xl font-semibold">Meet Timothy</h1>
              <p className="mt-4 text-sm text-muted-foreground">
                Timothy is your own assistant. It chats, runs long tasks called missions, remembers what matters, and can
                read your documents and accounts.
              </p>
              <p className="mt-2 text-sm text-muted-foreground">
                First, it needs a model to think with. This takes about two minutes.
              </p>
              <div className="mt-8 flex gap-2">
                <Button onClick={next}>Let's start</Button>
                <Button variant="outline" disabled={state.skipped} onClick={() => dispatch({ type: 'skip' })}>
                  Skip for now
                </Button>
              </div>
            </div>
          )}
          {state.step === 'source' && (
            <SourceStep onPick={(presetId) => dispatch({ type: 'pickSource', presetId })} />
          )}
          {state.step === 'provider' && preset && (
            <div>
              <h1 className="mb-6 text-xl font-semibold">Connect {preset.name}</h1>
              <ProviderAdd
                key={preset.id}
                presetId={preset.id}
                embedded
                onCreated={(id, name) => dispatch({ type: 'providerCreated', id, name })}
                onCancel={back}
              />
            </div>
          )}
          {state.step === 'verify' && state.provider && (
            <VerifyStep
              provider={state.provider}
              verified={state.verified}
              verifyError={state.verifyError}
              onVerified={onVerified}
              onFailed={onFailed}
              onRetry={() => {
                const p = state.provider
                if (p) dispatch({ type: 'providerCreated', id: p.id, name: p.name })
              }}
              onBack={back}
              onNext={next}
            />
          )}
          {state.step === 'roles' && <RolesStep onBack={back} onNext={next} />}
          {state.step === 'basics' && <BasicsStep onBack={back} onNext={next} />}
          {state.step === 'hello' && (
            <HelloStep
              busy={busy}
              onSend={(text) => void finish('/chat', { send: text } satisfies ChatIntent)}
              onExplore={() => void finish('/')}
            />
          )}
        </div>
      </main>
    </div>
  )
}
