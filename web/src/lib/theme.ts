export type Theme = 'light' | 'dark' | 'system'

const themeKey = 'timothy.theme'

export function getTheme(): Theme {
  const v = localStorage.getItem(themeKey)
  return v === 'light' || v === 'dark' ? v : 'system'
}

function prefersDark(): boolean {
  return window.matchMedia('(prefers-color-scheme: dark)').matches
}

// applyTheme stamps the .dark class on <html>; Tailwind's dark variant
// keys on it (see the @custom-variant in index.css).
export function applyTheme(t: Theme = getTheme()) {
  document.documentElement.classList.toggle('dark', t === 'dark' || (t === 'system' && prefersDark()))
}

export function setTheme(t: Theme) {
  localStorage.setItem(themeKey, t)
  applyTheme(t)
}

// watchSystemTheme keeps "system" live when the OS theme flips.
export function watchSystemTheme() {
  window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => applyTheme())
}

export const nextTheme: Record<Theme, Theme> = { system: 'light', light: 'dark', dark: 'system' }
