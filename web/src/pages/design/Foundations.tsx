import { statuses, statusMeta } from '@/components/timothy/status'
import { StatusBadge } from '@/components/timothy/status-badge'
import { StatusDot } from '@/components/timothy/status-dot'
import { SectionHeader, Eyebrow } from '@/components/timothy/page-header'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { cn } from '@/lib/utils'

const typeScale = [
  { name: 'Display', className: 'text-display font-semibold', sample: 'Good morning', meta: '28 / 36' },
  { name: 'Page title', className: 'text-title font-semibold', sample: 'Weekly GitHub digest', meta: '24 / 32' },
  { name: 'Section title', className: 'text-section font-semibold', sample: 'Provider health', meta: '16 / 24' },
  { name: 'Subsection title', className: 'text-sm font-semibold', sample: 'Goal', meta: '14 / 20' },
  { name: 'Eyebrow', className: 'text-xs font-medium tracking-[0.04em] uppercase text-muted-foreground', sample: 'Automations', meta: '12 / 16' },
  { name: 'Prose', className: 'text-prose', sample: 'Deliver a weekly digest of unread GitHub notifications.', meta: '15 / 24' },
  { name: 'Body', className: 'text-sm', sample: 'Discover · 3 of 5 units harness-passed', meta: '14 / 20' },
  { name: 'Secondary', className: 'text-sm text-muted-foreground', sample: 'Runs every Monday at 08:00.', meta: '14 / 20' },
  { name: 'Label', className: 'text-sm font-medium', sample: 'Default model', meta: '14 / 20' },
  { name: 'Metadata', className: 'text-xs text-muted-foreground tabular-nums', sample: '12 min ago · $0.0421', meta: '12 / 16' },
  { name: 'Code inline', className: 'text-sm', sample: 'stored at credential_ref', meta: '0.875em' },
  { name: 'Code block, prose', className: 'font-mono text-code', sample: 'go test ./internal/brain/missions/...', meta: '13 / 20' },
  { name: 'Code block, trace', className: 'font-mono text-trace', sample: '{ "tool": "search_web" }', meta: '12 / 18' },
  { name: 'Identifier', className: 'font-mono text-xs', sample: 'msn_01J8QK3F7ZC9', meta: '12 / 16' },
]

const colorGroups: { eyebrow: string; roles: { role: string; token: string }[] }[] = [
  {
    eyebrow: 'Neutral',
    roles: [
      { role: 'Background', token: 'bg-background' },
      { role: 'Card', token: 'bg-card' },
      { role: 'Muted', token: 'bg-muted' },
      { role: 'Accent', token: 'bg-accent' },
      { role: 'Border', token: 'bg-border' },
      { role: 'Input', token: 'bg-input' },
      { role: 'Ring', token: 'bg-ring' },
      { role: 'Primary', token: 'bg-primary' },
      { role: 'Secondary', token: 'bg-secondary' },
    ],
  },
  {
    eyebrow: 'Brand',
    roles: [
      { role: 'Brand', token: 'bg-brand' },
      { role: 'Brand soft', token: 'bg-brand-soft' },
      { role: 'Brand text', token: 'bg-brand-text' },
    ],
  },
  {
    eyebrow: 'Status',
    roles: [
      { role: 'Good', token: 'bg-good' },
      { role: 'Good soft', token: 'bg-good-soft' },
      { role: 'Warning', token: 'bg-warning' },
      { role: 'Warning soft', token: 'bg-warning-soft' },
      { role: 'Info', token: 'bg-info' },
      { role: 'Info soft', token: 'bg-info-soft' },
      { role: 'Destructive', token: 'bg-destructive' },
      { role: 'Destructive soft', token: 'bg-destructive-soft' },
    ],
  },
  {
    eyebrow: 'Chart',
    roles: [
      { role: 'Chart 1', token: 'bg-chart-1' },
      { role: 'Chart 2', token: 'bg-chart-2' },
      { role: 'Chart 3', token: 'bg-chart-3' },
      { role: 'Chart 4', token: 'bg-chart-4' },
      { role: 'Chart 5', token: 'bg-chart-5' },
    ],
  },
]

const statusMeaning: Record<(typeof statuses)[number], string> = {
  neutral: 'Not started, queued, disabled, unknown',
  working: 'Timothy or a job is running now',
  waiting: 'Blocked on the user: approval, answer, plan gate',
  success: 'Completed, healthy, passed',
  warning: 'Degraded, paused, near a limit, recoverable',
  error: 'Failed, unreachable, denied by policy',
}

const comfortableSteps = [
  { px: 6, label: 'Icon to label' },
  { px: 8, label: 'Label to control' },
  { px: 12, label: 'Card title to metadata' },
  { px: 16, label: 'Cards in a grid' },
  { px: 20, label: 'Field to field' },
  { px: 32, label: 'Fieldset to fieldset' },
  { px: 40, label: 'Section to section' },
]

const operationalSteps = [
  { px: 6, label: 'Log row padding' },
  { px: 8, label: 'Toolbar gap' },
  { px: 12, label: 'Panel padding' },
  { px: 24, label: 'Section to section' },
]

const radii = [
  { name: 'Square', className: 'rounded-md', label: 'Everything: card, button, input, badge, chip, bubble' },
  { name: 'Circle', className: 'rounded-full', label: 'Switch, radio, status dot, avatar, spinner' },
]

export function Foundations() {
  return (
    <div className="space-y-10">
      <section>
        <SectionHeader title="Typography scale" />
        <p className="mb-4 text-sm text-muted-foreground">Evaluate: hierarchy readable at a glance, four steps visible per page.</p>
        <div className="divide-y divide-border rounded-md border border-border">
          {typeScale.map((row) => (
            <div key={row.name} className="flex items-center justify-between gap-4 px-4 py-3">
              <div className="min-w-0">
                <Eyebrow className="mb-1 block normal-case tracking-normal text-muted-foreground/70">{row.name}</Eyebrow>
                <p className={cn('truncate', row.className)}>{row.sample}</p>
              </div>
              <span className="shrink-0 text-xs text-muted-foreground tabular-nums">{row.meta}</span>
            </div>
          ))}
        </div>
      </section>

      <section>
        <SectionHeader title="Colour roles" />
        <p className="mb-4 text-sm text-muted-foreground">Evaluate: orange scarcity, status hues distinct from brand.</p>
        <div className="space-y-6">
          {colorGroups.map((group) => (
            <div key={group.eyebrow}>
              <Eyebrow className="mb-3 block">{group.eyebrow}</Eyebrow>
              <div className="flex flex-wrap gap-4">
                {group.roles.map((swatch) => (
                  <div key={swatch.token} className="flex flex-col items-start gap-1.5">
                    <div className={cn('size-10 rounded-md border border-border', swatch.token)} />
                    <span className="text-sm">{swatch.role}</span>
                    <span className="font-mono text-xs text-muted-foreground">{swatch.token}</span>
                  </div>
                ))}
              </div>
            </div>
          ))}
        </div>
      </section>

      <section>
        <SectionHeader title="Status" />
        <p className="mb-4 text-sm text-muted-foreground">Evaluate: status communicated through text first, colour never alone.</p>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Status</TableHead>
              <TableHead>Badge</TableHead>
              <TableHead>Dot</TableHead>
              <TableHead>Icon</TableHead>
              <TableHead>Meaning</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {statuses.map((status) => {
              const meta = statusMeta[status]
              const Icon = meta.icon
              return (
                <TableRow key={status}>
                  <TableCell className="font-medium">{meta.label}</TableCell>
                  <TableCell>
                    <StatusBadge status={status} />
                  </TableCell>
                  <TableCell>
                    <StatusDot status={status} label={meta.label} showLabel />
                  </TableCell>
                  <TableCell>
                    <span className="inline-flex items-center gap-1.5">
                      <Icon aria-hidden className={cn('size-4', meta.spin && 'animate-spin motion-keep')} />
                      <span className="text-sm">{meta.label}</span>
                    </span>
                  </TableCell>
                  <TableCell className="text-sm text-muted-foreground">{statusMeaning[status]}</TableCell>
                </TableRow>
              )
            })}
          </TableBody>
        </Table>
      </section>

      <section>
        <SectionHeader title="Spacing" />
        <p className="mb-4 text-sm text-muted-foreground">Evaluate: two gap sizes far apart, never one gap everywhere.</p>
        <div className="space-y-6">
          <div>
            <Eyebrow className="mb-3 block">Comfortable</Eyebrow>
            <div className="space-y-3">
              {comfortableSteps.map((step) => (
                <div key={step.label} className="flex items-center gap-4">
                  <div className="bg-brand-soft" style={{ width: 64, height: step.px }} />
                  <span className="text-sm text-muted-foreground">
                    {step.px}px &middot; {step.label}
                  </span>
                </div>
              ))}
            </div>
          </div>
          <div>
            <Eyebrow className="mb-3 block">Operational</Eyebrow>
            <div className="space-y-3">
              {operationalSteps.map((step) => (
                <div key={step.label} className="flex items-center gap-4">
                  <div className="bg-brand-soft" style={{ width: 64, height: step.px }} />
                  <span className="text-sm text-muted-foreground">
                    {step.px}px &middot; {step.label}
                  </span>
                </div>
              ))}
            </div>
          </div>
        </div>
      </section>

      <section>
        <SectionHeader title="Radius" />
        <p className="mb-4 text-sm text-muted-foreground">Evaluate: radius stays quiet, used by role not by taste.</p>
        <div className="flex flex-wrap gap-6">
          {radii.map((radius) => (
            <div key={radius.name} className="flex flex-col items-start gap-1.5">
              <div className={cn('size-16 border border-border bg-card', radius.className)} />
              <span className="text-sm">{radius.name}</span>
              <span className="text-xs text-muted-foreground">{radius.label}</span>
            </div>
          ))}
        </div>
      </section>

      <section>
        <SectionHeader title="Elevation" />
        <p className="mb-4 text-sm text-muted-foreground">Evaluate: flat by default, shadow only for overlay and modal levels.</p>
        <div className="flex flex-wrap gap-6">
          <div className="flex flex-col items-start gap-1.5">
            <div className="size-24 rounded-md border border-border bg-card" />
            <span className="text-sm">Level 0 &middot; border only</span>
          </div>
          <div className="flex flex-col items-start gap-1.5">
            <div className="size-24 rounded-md border border-border bg-card shadow-overlay" />
            <span className="text-sm">Level 1 &middot; shadow-overlay</span>
          </div>
          <div className="flex flex-col items-start gap-1.5">
            <div className="size-24 rounded-md border border-border bg-card shadow-modal" />
            <span className="text-sm">Level 2 &middot; shadow-modal</span>
          </div>
        </div>
      </section>
    </div>
  )
}
