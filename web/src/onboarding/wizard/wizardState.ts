import type { OnboardingProgress, Readiness, TestResult } from '../../api/types'

export const wizardSteps = ['welcome', 'source', 'provider', 'verify', 'roles', 'basics', 'hello'] as const
export type WizardStep = (typeof wizardSteps)[number]

export interface WizardState {
  step: WizardStep
  presetId: string | null
  provider: { id: string; name: string } | null
  // verified is the passing check, verifyError the failed one's detail;
  // both null while the check runs.
  verified: TestResult | null
  verifyError: string | null
  skipped: boolean
}

export type WizardAction =
  | { type: 'next' }
  | { type: 'back' }
  | { type: 'pickSource'; presetId: string }
  | { type: 'providerCreated'; id: string; name: string }
  | { type: 'verified'; result: TestResult }
  | { type: 'verifyFailed'; detail: string }
  | { type: 'skip' }

// initialWizardState starts at the roles step when a chat model already
// works: there is nothing to connect.
export function initialWizardState(chatReady: boolean): WizardState {
  return {
    step: chatReady ? 'roles' : 'welcome',
    presetId: null,
    provider: null,
    verified: null,
    verifyError: null,
    skipped: false,
  }
}

const nextStep: Record<WizardStep, WizardStep> = {
  welcome: 'source',
  source: 'provider',
  provider: 'verify',
  verify: 'roles',
  roles: 'basics',
  basics: 'hello',
  hello: 'hello',
}

// The created provider stays created, so back from roles skips the
// one-off check and returns to the provider picker.
const backStep: Record<WizardStep, WizardStep> = {
  welcome: 'welcome',
  source: 'welcome',
  provider: 'source',
  verify: 'provider',
  roles: 'source',
  basics: 'roles',
  hello: 'basics',
}

export function wizardReducer(state: WizardState, action: WizardAction): WizardState {
  switch (action.type) {
    case 'next':
      // source and provider advance only through pickSource and
      // providerCreated, which carry what the next step needs.
      if (state.step === 'source' || state.step === 'provider') return state
      return { ...state, step: nextStep[state.step] }
    case 'back':
      return { ...state, step: backStep[state.step] }
    case 'pickSource':
      return { ...state, step: 'provider', presetId: action.presetId }
    case 'providerCreated':
      return {
        ...state,
        step: 'verify',
        provider: { id: action.id, name: action.name },
        verified: null,
        verifyError: null,
      }
    case 'verified':
      return { ...state, verified: action.result, verifyError: null }
    case 'verifyFailed':
      return { ...state, verified: null, verifyError: action.detail }
    case 'skip':
      return { ...state, skipped: true }
  }
}

// shouldRedirectToWelcome sends a fresh install from Home to the
// wizard. Only from '/', so deep links still land where they point.
export function shouldRedirectToWelcome(
  readiness: Readiness | null,
  progress: OnboardingProgress,
  pathname: string,
): boolean {
  return (
    pathname === '/' &&
    readiness !== null &&
    !readiness.chat_route &&
    progress.wizard !== 'skipped' &&
    progress.wizard !== 'done'
  )
}
