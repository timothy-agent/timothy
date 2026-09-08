// BrandMark: the pixel-art brand mark, inlined as SVG (not an <img>
// reference to /favicon.svg) so it stays crisp and renders the same
// regardless of theme. The whole mark is pixels, not just the letter:
// every cell of the 8x8 grid is its own 6-unit block, the ground
// alternating two near-black tones so the grid reads at a glance, the
// T in brand green. crispEdges keeps every block edge hard. Same grid
// as public/favicon.svg.
export function BrandMark({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 48 48" aria-hidden="true" shapeRendering="crispEdges" className={className}>
      <rect x="0" y="0" width="6" height="6" fill="#080c09" />
      <rect x="6" y="0" width="6" height="6" fill="#17241a" />
      <rect x="12" y="0" width="6" height="6" fill="#080c09" />
      <rect x="18" y="0" width="6" height="6" fill="#17241a" />
      <rect x="24" y="0" width="6" height="6" fill="#080c09" />
      <rect x="30" y="0" width="6" height="6" fill="#17241a" />
      <rect x="36" y="0" width="6" height="6" fill="#080c09" />
      <rect x="42" y="0" width="6" height="6" fill="#17241a" />
      <rect x="0" y="6" width="6" height="6" fill="#17241a" />
      <rect x="6" y="6" width="6" height="6" fill="#080c09" />
      <rect x="12" y="6" width="6" height="6" fill="#17241a" />
      <rect x="18" y="6" width="6" height="6" fill="#080c09" />
      <rect x="24" y="6" width="6" height="6" fill="#17241a" />
      <rect x="30" y="6" width="6" height="6" fill="#080c09" />
      <rect x="36" y="6" width="6" height="6" fill="#17241a" />
      <rect x="42" y="6" width="6" height="6" fill="#080c09" />
      <rect x="0" y="12" width="6" height="6" fill="#17241a" />
      <rect x="6" y="12" width="6" height="6" fill="#080c09" />
      <rect x="12" y="12" width="6" height="6" fill="#00e654" />
      <rect x="18" y="12" width="6" height="6" fill="#00e654" />
      <rect x="24" y="12" width="6" height="6" fill="#00e654" />
      <rect x="30" y="12" width="6" height="6" fill="#00e654" />
      <rect x="36" y="12" width="6" height="6" fill="#080c09" />
      <rect x="42" y="12" width="6" height="6" fill="#17241a" />
      <rect x="0" y="18" width="6" height="6" fill="#080c09" />
      <rect x="6" y="18" width="6" height="6" fill="#17241a" />
      <rect x="12" y="18" width="6" height="6" fill="#080c09" />
      <rect x="18" y="18" width="6" height="6" fill="#00e654" />
      <rect x="24" y="18" width="6" height="6" fill="#00e654" />
      <rect x="30" y="18" width="6" height="6" fill="#080c09" />
      <rect x="36" y="18" width="6" height="6" fill="#17241a" />
      <rect x="42" y="18" width="6" height="6" fill="#080c09" />
      <rect x="0" y="24" width="6" height="6" fill="#17241a" />
      <rect x="6" y="24" width="6" height="6" fill="#080c09" />
      <rect x="12" y="24" width="6" height="6" fill="#17241a" />
      <rect x="18" y="24" width="6" height="6" fill="#00e654" />
      <rect x="24" y="24" width="6" height="6" fill="#00e654" />
      <rect x="30" y="24" width="6" height="6" fill="#17241a" />
      <rect x="36" y="24" width="6" height="6" fill="#080c09" />
      <rect x="42" y="24" width="6" height="6" fill="#17241a" />
      <rect x="0" y="30" width="6" height="6" fill="#080c09" />
      <rect x="6" y="30" width="6" height="6" fill="#17241a" />
      <rect x="12" y="30" width="6" height="6" fill="#080c09" />
      <rect x="18" y="30" width="6" height="6" fill="#00e654" />
      <rect x="24" y="30" width="6" height="6" fill="#00e654" />
      <rect x="30" y="30" width="6" height="6" fill="#080c09" />
      <rect x="36" y="30" width="6" height="6" fill="#17241a" />
      <rect x="42" y="30" width="6" height="6" fill="#080c09" />
      <rect x="0" y="36" width="6" height="6" fill="#17241a" />
      <rect x="6" y="36" width="6" height="6" fill="#080c09" />
      <rect x="12" y="36" width="6" height="6" fill="#17241a" />
      <rect x="18" y="36" width="6" height="6" fill="#080c09" />
      <rect x="24" y="36" width="6" height="6" fill="#17241a" />
      <rect x="30" y="36" width="6" height="6" fill="#080c09" />
      <rect x="36" y="36" width="6" height="6" fill="#17241a" />
      <rect x="42" y="36" width="6" height="6" fill="#080c09" />
      <rect x="0" y="42" width="6" height="6" fill="#080c09" />
      <rect x="6" y="42" width="6" height="6" fill="#17241a" />
      <rect x="12" y="42" width="6" height="6" fill="#080c09" />
      <rect x="18" y="42" width="6" height="6" fill="#17241a" />
      <rect x="24" y="42" width="6" height="6" fill="#080c09" />
      <rect x="30" y="42" width="6" height="6" fill="#17241a" />
      <rect x="36" y="42" width="6" height="6" fill="#080c09" />
      <rect x="42" y="42" width="6" height="6" fill="#17241a" />
    </svg>
  )
}
