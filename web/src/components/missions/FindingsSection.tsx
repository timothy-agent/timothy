import type { ReviewFinding } from '../../api/types'

// severityClass mirrors the harness severities (missions.Finding,
// D-092): blocking prevents approval, minor is advisory.
function severityClass(f: ReviewFinding): string {
  return f.severity === 'minor'
    ? 'bg-muted text-muted-foreground'
    : 'bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300'
}

// FindingsSection lists the mission's review findings ledger: id,
// severity, file, title, and the reviewer's quoted evidence line
// (D-095). Resolved and accepted findings render struck through.
export function FindingsSection({ findings }: { findings: ReviewFinding[] }) {
  if (findings.length === 0) {
    return null
  }
  return (
    <ul className="space-y-1.5 text-sm">
      {findings.map((f) => {
        const closed = f.status !== undefined && f.status !== 'open'
        return (
          <li key={f.id} className={closed ? 'text-muted-foreground line-through' : undefined}>
            <div className="flex items-center gap-2">
              <span className="shrink-0 font-mono text-xs">{f.id}</span>
              <span className={`shrink-0 rounded px-1.5 py-0.5 text-xs font-medium ${severityClass(f)}`}>
                {f.severity ?? 'blocking'}
              </span>
              {f.file && <span className="shrink-0 font-mono text-xs">{f.file}</span>}
              <span>{f.title}</span>
            </div>
            {f.evidence && (
              <pre className="ml-4 mt-0.5 overflow-x-auto whitespace-pre-wrap font-mono text-xs text-muted-foreground">
                {f.evidence}
              </pre>
            )}
          </li>
        )
      })}
    </ul>
  )
}
