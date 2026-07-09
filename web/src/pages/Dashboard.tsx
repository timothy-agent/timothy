import { PagePlaceholder } from '../components/PagePlaceholder'

const stats = [
  { label: 'Spend today', hint: 'USD from the cost ledger' },
  { label: 'Tokens', hint: 'input / output across providers' },
  { label: 'Requests', hint: 'by provider and category' },
  { label: 'P95 latency', hint: 'per provider' },
]

export function Dashboard() {
  return (
    <PagePlaceholder
      title="Dashboard"
      description="Spend, tokens, and latency derived from the cost ledger. Populates when usage metrics land."
    >
      <div className="mt-8 grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
        {stats.map((s) => (
          <div
            key={s.label}
            className="rounded-xl border border-zinc-200 p-5 dark:border-zinc-800"
          >
            <div className="text-sm text-zinc-500 dark:text-zinc-400">{s.label}</div>
            <div className="mt-2 text-3xl font-semibold text-zinc-300 dark:text-zinc-600">—</div>
            <div className="mt-1 text-xs text-zinc-400 dark:text-zinc-500">{s.hint}</div>
          </div>
        ))}
      </div>
    </PagePlaceholder>
  )
}
