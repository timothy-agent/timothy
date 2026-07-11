import { ArrowDown01Icon, Tick02Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { categoryMeta, categoryOrder, type Category } from '../lib/categories'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from './ui/dropdown-menu'

// CategoryPicker steers which route chain serves the next message. It
// reads as a mode picker — Standard / Fast / Deep with plain-language
// descriptions and a relative cost — so choosing needs no knowledge
// of the routing table underneath.
export function CategoryPicker({
  value,
  onChange,
}: {
  value: string
  onChange: (v: string) => void
}) {
  const current = categoryMeta[value as Category] ?? categoryMeta.coding
  const everyday = categoryOrder.filter((c) => !categoryMeta[c].specialized)
  const specialized = categoryOrder.filter((c) => categoryMeta[c].specialized)

  const item = (c: Category) => {
    const meta = categoryMeta[c]
    const selected = c === value
    return (
      <DropdownMenuItem
        key={c}
        onSelect={() => onChange(c)}
        data-selected={selected || undefined}
        className="items-start gap-3 rounded-lg px-2.5 py-2 data-selected:bg-zinc-100 dark:data-selected:bg-zinc-800"
      >
        <HugeiconsIcon icon={meta.icon} className="mt-0.5 size-4.5 shrink-0" />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <span className="text-sm font-medium">{meta.label}</span>
            <span className="rounded bg-zinc-100 px-1.5 py-px text-[10px] font-medium text-zinc-500 dark:bg-zinc-800 dark:text-zinc-400">
              {meta.cost}
            </span>
            {meta.recommended && (
              <span className="rounded bg-blue-500/10 px-1.5 py-px text-[10px] font-medium text-blue-600 dark:text-blue-400">
                Recommended
              </span>
            )}
          </div>
          <p className="mt-0.5 text-xs text-muted-foreground">{meta.description}</p>
        </div>
        {selected && <HugeiconsIcon icon={Tick02Icon} className="mt-1 size-4 shrink-0" />}
      </DropdownMenuItem>
    )
  }

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        aria-label="Task category"
        className="flex h-8 items-center gap-1.5 rounded-full border border-zinc-950/10 px-3 text-sm text-zinc-700 transition hover:bg-zinc-100 dark:border-white/10 dark:text-zinc-200 dark:hover:bg-zinc-700/50"
      >
        <HugeiconsIcon icon={current.icon} className="size-4" />
        <span>{current.label}</span>
        <HugeiconsIcon icon={ArrowDown01Icon} className="size-3.5 opacity-60" />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-[min(92vw,22rem)] p-1.5">
        <DropdownMenuLabel className="px-2.5 pt-1.5">
          <div className="text-sm font-semibold">Mode</div>
          <p className="mt-0.5 text-xs font-normal text-muted-foreground">
            Picks the models serving your next message. Faster modes cost less; deeper modes think
            harder.
          </p>
        </DropdownMenuLabel>
        {everyday.map(item)}
        <DropdownMenuSeparator />
        <DropdownMenuLabel className="px-2.5 text-xs font-medium text-muted-foreground">
          Specialized
        </DropdownMenuLabel>
        {specialized.map(item)}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
