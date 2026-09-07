import type { ReviewFinding } from '../../api/types'
import { StatusBadge } from '../timothy/status-badge'
import type { Status } from '../timothy/status'

// severityStatus mirrors the harness severities (missions.Finding,
// D-092): blocking prevents approval, minor is advisory.
function severityStatus(f: ReviewFinding): Status {
  return f.severity === 'minor' ? 'neutral' : 'warning'
}

// FindingsSection lists the mission's review findings ledger: id,
// severity, file, title, and the reviewer's quoted evidence line
// (D-095). Resolved and accepted findings render struck through.
export function FindingsSection({ findings }: { findings: ReviewFinding[] }) {
  if (findings.length === 0) {
    return null
  }
  return (
    <ul className="divide-y divide-border text-sm">
      {findings.map((f) => {
        const closed = f.status !== undefined && f.status !== 'open'
        return (
          <li key={f.id} className={`py-2 ${closed ? 'text-muted-foreground line-through' : ''}`}>
            <div className="flex items-center gap-2">
              <span className="shrink-0 font-mono text-xs">{f.id}</span>
              <StatusBadge status={severityStatus(f)} label={f.severity ?? 'blocking'} size="sm" />
              {f.file && <span className="shrink-0 font-mono text-xs">{f.file}</span>}
              <span>{f.title}</span>
            </div>
            {f.evidence && (
              <pre className="ml-4 mt-0.5 overflow-x-auto whitespace-pre-wrap font-mono text-trace text-muted-foreground">
                {f.evidence}
              </pre>
            )}
          </li>
        )
      })}
    </ul>
  )
}
