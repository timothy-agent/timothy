// Shared ECharts theme fragment built from the app's own CSS custom
// properties (index.css design tokens) — canvas accepts oklch() strings
// directly in modern browsers, so these are passed through as-is
// rather than converted.

function cssVar(name: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim()
}

export function isDarkTheme(): boolean {
  return document.documentElement.classList.contains('dark')
}

// buildBaseTheme reads current token values — call again after a theme
// flip rather than caching the result.
export function buildBaseTheme() {
  const foreground = cssVar('--foreground')
  const muted = cssVar('--muted-foreground')
  const border = cssVar('--border')
  const popover = cssVar('--popover')
  const fontFamily = 'Geist Variable, sans-serif'

  return {
    textStyle: { color: foreground, fontFamily },
    axisLine: { lineStyle: { color: border } },
    axisLabel: { color: muted, fontFamily, fontSize: 10.5 },
    axisTick: { lineStyle: { color: border } },
    splitLine: { lineStyle: { color: border, opacity: 0.5 } },
    tooltip: {
      backgroundColor: popover,
      borderColor: border,
      textStyle: { color: foreground, fontFamily, fontSize: 12 },
    },
  }
}
