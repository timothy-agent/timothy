import { Mail01Icon, GlobalIcon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import type { ReactNode } from 'react'
import { TelegramIcon } from '@/components/icons/TelegramIcon'
import type { Destination } from '../../api/types'

const destinationKindIcon = { email: Mail01Icon, webhook: GlobalIcon } as const

// DestinationKindIcon renders the small glyph identifying a
// destination's kind — shared by the settings destinations list and
// the automations pages' destination badges.
export function DestinationKindIcon({
  kind,
  className = 'size-3.5',
}: {
  kind: Destination['kind']
  className?: string
}) {
  if (kind === 'telegram') return <TelegramIcon className={className} />
  return <HugeiconsIcon icon={destinationKindIcon[kind]} className={className} />
}

// Toggle is a dependency-free switch that reads in both themes.
export function Toggle({
  on,
  onChange,
  label,
}: {
  on: boolean
  onChange: (v: boolean) => void
  label: string
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={on}
      aria-label={label}
      onClick={() => onChange(!on)}
      className={`relative h-6 w-10 rounded-full transition ${on ? 'bg-blue-600' : 'bg-zinc-300 dark:bg-zinc-700'}`}
    >
      <span
        className={`absolute top-0.5 size-5 rounded-full bg-white shadow transition-all ${on ? 'left-4.5' : 'left-0.5'}`}
      />
    </button>
  )
}

export function ErrorBanner({ message }: { message: string | null }) {
  if (!message) return null
  return (
    <div className="rounded-xl border border-red-500/30 bg-red-500/5 p-3 text-sm text-red-600 dark:text-red-400">
      {message}
    </div>
  )
}

export function Field({
  label,
  hint,
  className,
  children,
}: {
  label: string
  hint?: string
  className?: string
  children: ReactNode
}) {
  return (
    <label className={`block text-sm font-medium text-foreground ${className ?? ''}`}>
      {label}
      {hint && <span className="ml-1.5 font-normal text-muted-foreground">· {hint}</span>}
      {children}
    </label>
  )
}

