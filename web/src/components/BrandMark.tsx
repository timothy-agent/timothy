// BrandMark: the orange-tile "T" brand mark, inlined as SVG (not an
// <img> reference to /favicon.svg) so it stays crisp and renders the
// same regardless of theme. Same gradient + path as public/favicon.svg.
export function BrandMark({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 48 48" aria-hidden="true" className={className}>
      <defs>
        <linearGradient id="brand-mark-g" x1="0" y1="0" x2="1" y2="1">
          <stop offset="0" stopColor="#ff9d4d" />
          <stop offset="1" stopColor="#f4700e" />
        </linearGradient>
      </defs>
      <rect width="48" height="48" rx="10" fill="url(#brand-mark-g)" />
      <path
        d="M9 8h30a2.5 2.5 0 0 1 2.5 2.5v3a2.5 2.5 0 0 1-2.5 2.5H28v22a2.5 2.5 0 0 1-2.5 2.5h-3a2.5 2.5 0 0 1-2.5-2.5V16H9a2.5 2.5 0 0 1-2.5-2.5v-3A2.5 2.5 0 0 1 9 8Z"
        fill="#fff"
      />
    </svg>
  )
}
