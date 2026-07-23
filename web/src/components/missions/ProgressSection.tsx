import type { ProgressNote } from '../../api/types'

export function ProgressSection({ notes }: { notes: ProgressNote[] }) {
  if (notes.length === 0) {
    return <p className="text-sm text-muted-foreground">No progress notes yet.</p>
  }
  return (
    <ul className="space-y-2">
      {notes.map((n, i) => (
        <li key={i} className="text-sm">
          <span className="mr-2 text-xs text-muted-foreground">{new Date(n.at).toLocaleString()}</span>
          {n.note}
        </li>
      ))}
    </ul>
  )
}
