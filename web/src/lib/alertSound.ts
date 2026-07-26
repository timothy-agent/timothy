// playAlertSound synthesizes a short two-tone chime via the Web Audio
// API — no asset file needed, and it works the moment the tab has had
// any user interaction (autoplay policies block audio otherwise, which
// is unavoidable and fine: the visual banner still shows regardless).
export function playAlertSound(): void {
  try {
    const Ctx = window.AudioContext ?? (window as unknown as { webkitAudioContext?: typeof AudioContext }).webkitAudioContext
    if (!Ctx) return
    const ctx = new Ctx()
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
    setTimeout(() => void ctx.close(), 400)
  } catch {
    // Audio isn't available (unsupported browser, blocked autoplay) —
    // the visual permission banner is the fallback, never worth erroring over.
  }
}
