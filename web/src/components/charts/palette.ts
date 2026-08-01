// Validated categorical palette (dataviz skill): fixed hue order, never
// cycled past slot 8 — CVD-safe adjacent pairs in both light and dark.
export const palette = ['#2a78d6', '#eb6834', '#1baf7a', '#eda100', '#e87ba4', '#008300', '#4a3aa7', '#e34948']

export function niceMax(v: number): number {
  const mag = Math.pow(10, Math.floor(Math.log10(v || 1)))
  const n = v / mag
  const step = n <= 1 ? 1 : n <= 2 ? 2 : n <= 5 ? 5 : 10
  return step * mag
}
