// CursorIcon is a monochrome cursor-arrow glyph standing in for the
// Cursor CLI, via currentColor. Inline SVG only — CSP forbids fetching
// an external asset.
export function CursorIcon({ className = 'size-3.5' }: { className?: string }) {
  return (
    <svg viewBox="0 0 24 24" className={`${className} shrink-0`} aria-hidden="true">
      <path fill="currentColor" d="M4 2L20 12L13 13.5L10.5 20.5L4 2Z" />
    </svg>
  )
}
