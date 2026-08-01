// sharedCtx is one AudioContext reused for every chime, not a fresh
// one per call: a context created outside a user-gesture handler
// starts 'suspended' under autoplay policy and a later resume() still
// needs SOME prior gesture to have unlocked it — unlockAudio (called
// from a real click elsewhere in the app) is what actually flips it
// live. Creating fresh contexts per play defeats that: each one starts
// suspended again with no gesture of its own to unlock it.
let sharedCtx: AudioContext | undefined

function getCtx(): AudioContext | undefined {
  if (sharedCtx) return sharedCtx
  const Ctx = window.AudioContext ?? (window as unknown as { webkitAudioContext?: typeof AudioContext }).webkitAudioContext
  if (!Ctx) return undefined
  sharedCtx = new Ctx()
  return sharedCtx
}

// unlockAudio resumes the shared context from inside a real user
// gesture (a click anywhere in the app) — call it once on the app's
// first pointerdown/keydown so a LATER chime triggered by a background
// event (an SSE signal, no gesture of its own) isn't silently blocked
// by autoplay policy. Safe to call repeatedly; a no-op once resumed.
export function unlockAudio(): void {
  try {
    getCtx()?.resume()
  } catch {
    // Unsupported/blocked — playAlertSound's own try/catch covers the
    // actual chime attempt; this is best-effort priming only.
  }
}

// playAlertSound synthesizes a short two-tone chime via the Web Audio
// API — no asset file needed. Reliable only after unlockAudio has run
// from a real gesture (autoplay policy blocks it otherwise); the
// visual banner is the fallback either way, never worth erroring over.
export function playAlertSound(): void {
  try {
    const ctx = getCtx()
    if (!ctx) return
    const now = ctx.currentTime
    ;[880, 660].forEach((freq, i) => {
      const osc = ctx.createOscillator()
      const gain = ctx.createGain()
      osc.type = 'sine'
      osc.frequency.value = freq
      const start = now + i * 0.12
      gain.gain.setValueAtTime(0.0001, start)
      gain.gain.exponentialRampToValueAtTime(0.2, start + 0.01)
      gain.gain.exponentialRampToValueAtTime(0.0001, start + 0.11)
      osc.connect(gain)
      gain.connect(ctx.destination)
      osc.start(start)
      osc.stop(start + 0.12)
    })
  } catch {
    // Audio isn't available (unsupported browser, blocked autoplay) —
    // the visual permission banner is the fallback, never worth erroring over.
  }
}
