import { useState, type ReactNode } from 'react'
import { ChevronDown, ChevronUp } from 'lucide-react'
import type { EnvFacts } from '../../api/types'
import { IconButton } from '../timothy/icon-button'
import { Panel } from '../timothy/panel'

// manifestDirs groups manifest paths by directory, sorted, "." for the
// repo root, the same grouping the prompt block uses.
function manifestDirs(manifests: string[]): [string, string[]][] {
  const byDir = new Map<string, string[]>()
  for (const p of manifests) {
    const i = p.lastIndexOf('/')
    const dir = i < 0 ? '.' : p.slice(0, i)
    byDir.set(dir, [...(byDir.get(dir) ?? []), p.slice(i + 1)])
  }
  return [...byDir.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([d, names]) => [d, names.sort()])
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid grid-cols-[8rem_1fr] gap-2">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0 break-words">{children}</dd>
    </div>
  )
}

// EnvFactsSection shows the environment facts the harness probed at
// provisioning (issue #1008), read-only and collapsed by default like
// DiscoverSection. The page mounts it only when facts exist.
export function EnvFactsSection({ facts }: { facts: EnvFacts }) {
  const [expanded, setExpanded] = useState(false)
  const destinations = facts.destinations ?? []
  const manifests = facts.manifests ?? []
  const installed = (facts.tools ?? []).filter((t) => t.version)
  const absent = (facts.tools ?? []).filter((t) => !t.version)

  const actions = (
    <IconButton
      size="xs"
      label={expanded ? 'Hide environment' : 'Show environment'}
      icon={expanded ? ChevronUp : ChevronDown}
      aria-expanded={expanded}
      onClick={() => setExpanded((v) => !v)}
    />
  )

  return (
    <Panel title="Environment" actions={actions}>
      {expanded && (
        <dl className="space-y-1.5 text-sm">
          {facts.base_branch && (
            <Row label="Base branch">
              <code>{facts.base_branch}</code>
            </Row>
          )}
          <Row label="Delivery">
            {destinations.length > 0
              ? destinations.map((d) => `${d.kind} (${d.mode || 'mode unknown'})`).join(', ')
              : 'No repository destination'}
          </Row>
          {manifests.length > 0 && (
            <Row label="Manifests">
              <ul className="space-y-0.5">
                {manifestDirs(manifests).map(([dir, names]) => (
                  <li key={dir}>
                    <code>{dir}</code>: {names.join(', ')}
                  </li>
                ))}
              </ul>
            </Row>
          )}
          {installed.length > 0 && (
            <Row label="Tools">
              <ul className="space-y-0.5">
                {installed.map((t) => (
                  <li key={t.name}>
                    {t.name}: <span className="text-muted-foreground">{t.version}</span>
                  </li>
                ))}
              </ul>
            </Row>
          )}
          {absent.length > 0 && <Row label="Not installed">{absent.map((t) => t.name).join(', ')}</Row>}
          {(facts.gaps ?? []).map((g) => (
            <Row key={g} label="Gap">
              {g}
            </Row>
          ))}
        </dl>
      )}
    </Panel>
  )
}
