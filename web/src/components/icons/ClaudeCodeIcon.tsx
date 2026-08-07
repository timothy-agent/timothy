// ClaudeCodeIcon is Anthropic's Claude spark mark (simplified 8-ray
// starburst), single-color, for the mission detail harness pill.
// Inline SVG only — CSP forbids fetching an external asset.
export function ClaudeCodeIcon({ className = 'size-3.5' }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 24 24"
      className={`${className} shrink-0`}
      fill="#D97757"
      aria-hidden="true"
    >
      <path d="M12 0l1.6 8.4L22 6l-6.4 5.6L24 12l-8.4 1.6L22 22l-6.4-8.4L12 24l-1.6-8.4L2 22l6.4-8.4L0 12l8.4-1.6L2 2l6.4 8.4L12 0z" />
    </svg>
  )
}
