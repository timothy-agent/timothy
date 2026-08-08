// PiIcon is the official pi coding agent badge (pi.dev/favicon.svg,
// the press kit's "square mark for favicons and compact badges"),
// colors as published. Inline SVG only — CSP forbids fetching an
// external asset.
export function PiIcon({ className = 'size-3.5' }: { className?: string }) {
  return (
    <svg viewBox="0 0 800 800" className={`${className} shrink-0`} aria-hidden="true">
      <rect width="800" height="800" rx="120" fill="#09090b" />
      <path
        fill="#fff"
        fillRule="evenodd"
        d="M165.29 165.29H517.36V400H400V517.36H282.65V634.72H165.29ZM282.65 282.65V400H400V282.65Z"
      />
      <path fill="#fff" d="M517.36 400H634.72V634.72H517.36Z" />
    </svg>
  )
}
