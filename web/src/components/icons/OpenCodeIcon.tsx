// OpenCodeIcon is the official opencode terminal-block mark
// (opencode.ai/favicon.svg), monochrome via currentColor. Inline SVG
// only — CSP forbids fetching an external asset.
export function OpenCodeIcon({ className = 'size-3.5' }: { className?: string }) {
  return (
    <svg viewBox="0 0 512 512" className={`${className} shrink-0`} aria-hidden="true">
      <path
        fill="currentColor"
        fillRule="evenodd"
        clipRule="evenodd"
        d="M384 416H128V96H384V416ZM320 160H192V352H320V160Z"
      />
      <path fill="currentColor" opacity="0.45" d="M320 224V352H192V224H320Z" />
    </svg>
  )
}
