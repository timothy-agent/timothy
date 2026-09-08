import { Check, Hand, Loader2 } from 'lucide-react'

import { SectionHeader, Eyebrow } from '@/components/timothy/page-header'
import { Panel } from '@/components/timothy/panel'
import { StatusBadge } from '@/components/timothy/status-badge'
import { JsonBlock } from '@/components/timothy/json-block'
import { Kbd } from '@/components/timothy/kbd'
import { TraceGroup, TraceRow, traceIcons } from '@/components/timothy/trace-group'
import { EventLog, type EventLogRow } from '@/components/timothy/event-log'
import { BrandTile } from '@/components/timothy/brand-tile'
import { ApprovalCardShell } from '@/components/timothy/approval-card'
import { ProviderLogo, presetForProviderName } from '@/components/timothy/provider-logo'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { Progress } from '@/components/ui/progress'
import { cn } from '@/lib/utils'
import { eventLogComponentRows, providers, timothyAnswerParagraphs } from './fixtures'

// title is a parameter so repeated specimens on the page (used inside
// ConversationSample and standalone) get distinct accessible names:
// axe's landmark-unique rule flags two role="region" landmarks with
// the same labelledby text.
function ApprovalSample({ title = 'Timothy wants to run a command' }: { title?: string }) {
  return (
    <ApprovalCardShell
      title={title}
      meta="08:04"
      rationale="Running the missions package tests confirms the verifier change didn't regress batch verification."
      consequence="Runs inside the mission sandbox. Can modify files in the worktree. Sends no external traffic."
      actions={
        <>
          <Button variant="outline">
            Deny
            <Kbd>D</Kbd>
          </Button>
          <Button variant="outline">
            Allow for session
            <Kbd>S</Kbd>
          </Button>
          <Button>
            Allow once
            <Kbd>A</Kbd>
          </Button>
        </>
      }
    >
      <div>
        <Eyebrow className="mb-1.5 block">What</Eyebrow>
        <JsonBlock value={{ tool: 'shell', cmd: 'go test ./internal/brain/missions/...' }} density="prose" copy={false} />
      </div>
    </ApprovalCardShell>
  )
}

// approvalTitle lets each ConversationSample render (Conversation
// section, Side by side section) give its embedded ApprovalSample a
// distinct accessible name; see the note on ApprovalSample.
function ConversationSample({ approvalTitle }: { approvalTitle: string }) {
  return (
    <div className="max-w-[45rem] space-y-8">
      <div className="flex justify-end">
        <div className="max-w-[85%] rounded-md bg-muted px-4 py-3 text-prose">
          Can you look into why the knowledge base ingest is slower than last week?
        </div>
      </div>
      <div>
        <div className="flex items-center gap-2">
          <BrandTile
            size="sm"
            className="size-5 bg-brand text-brand-foreground"
            icon={<span className="text-xs font-semibold">T</span>}
            label="Timothy"
          />
          <span className="text-xs text-muted-foreground">08:03</span>
        </div>
        <div className="mt-3 space-y-3 text-prose">
          <p>{timothyAnswerParagraphs[0]}</p>
        </div>
        <div className="mt-3">
          <TraceGroup summary="3 tool calls" duration="2.1s">
            <TraceRow status="success" icon={traceIcons.web} action="Search web" target="pgvector chunking rrf" duration="640ms" />
            <TraceRow status="success" icon={traceIcons.file} action="Read" target="internal/memory/chunk/split.go" duration="12ms">
              <JsonBlock value={{ path: 'internal/memory/chunk/split.go', lines: 214 }} density="trace" copy={false} />
            </TraceRow>
            <TraceRow status="error" icon={traceIcons.shell} action="Run" target="go test ./internal/memory/chunk/..." duration="1.4s">
              <p className="text-xs text-destructive">FAIL: TestSplitOverlap, expected 3 chunks, got 4</p>
            </TraceRow>
          </TraceGroup>
        </div>
        <div className="mt-3 space-y-3 text-prose">
          <p>{timothyAnswerParagraphs[1]}</p>
          <p>{timothyAnswerParagraphs[2]}</p>
        </div>
      </div>
      <ApprovalSample title={approvalTitle} />
    </div>
  )
}

function AgentStatusLineSample() {
  return (
    <div className="space-y-2">
      <div role="status" className="flex h-9 items-center gap-2 text-sm">
        <Loader2 className="size-4 animate-spin motion-keep text-info" aria-hidden />
        <span>Thinking</span>
      </div>
      <div role="status" className="flex h-9 items-center gap-2 text-sm">
        <Loader2 className="size-4 animate-spin motion-keep text-info" aria-hidden />
        <span>
          Running <code>search_web</code>
        </span>
      </div>
      <div role="status" className="flex h-9 items-center gap-2 text-sm">
        <Hand className="size-4 text-warning" aria-hidden />
        <span>Waiting for your approval</span>
        <Button variant="link" size="xs">
          Review
        </Button>
      </div>
      <div role="status" className="flex h-9 items-center gap-2 text-sm">
        <Check className="size-4 text-good" aria-hidden />
        <span>Worked for 4.2s &middot; 3 tools</span>
      </div>
    </div>
  )
}

function eventLogComponentRowIcon(kind: string) {
  if (kind === 'tool') return traceIcons.shell
  if (kind === 'review') return traceIcons.other
  return undefined
}

// eventLogSampleRows adapts the fixture data into EventLogRow[], the
// exported shape (component A2) rather than the older hand-rolled
// EventLogSample above. The "turn" row nests a ToolCallGroup-style
// TraceRow pair to show a disclosure with children (14.11).
const eventLogSampleRows: EventLogRow[] = eventLogComponentRows.map((r) => ({
  id: r.id,
  time: new Date(r.time),
  kind: r.kind,
  icon: eventLogComponentRowIcon(r.kind),
  label: r.label,
  title:
    r.target != null ? (
      <>
        {r.text} <code className="font-mono">{r.target}</code>
      </>
    ) : (
      r.text
    ),
  payload: r.kind !== 'turn' ? r.payload : undefined,
  payloadNode:
    r.kind === 'turn' ? (
      <div className="space-y-0.5">
        <TraceRow status="success" icon={traceIcons.mail} action="List calendar events" target="account=work" duration="640ms" />
        <TraceRow status="success" icon={traceIcons.web} action="Search web" target="unread github notifications api" duration="420ms" />
      </div>
    ) : undefined,
}))

function EventLogComponentSample() {
  return (
    <Panel title="Timeline" density="operational" headingLevel="h3">
      <EventLog rows={eventLogSampleRows} />
    </Panel>
  )
}

const phaseSteps = ['Discover', 'Plan', 'Build', 'Prove', 'Result']

function PhaseStepper({ current }: { current: string }) {
  const currentIndex = phaseSteps.indexOf(current)
  return (
    <ol className="flex items-center gap-2">
      {phaseSteps.map((step, i) => {
        const done = i < currentIndex
        const isCurrent = i === currentIndex
        return (
          <li key={step} className="flex items-center gap-2">
            {i > 0 && <span aria-hidden className="size-1 rounded-full bg-border" />}
            <span
              aria-current={isCurrent ? 'step' : undefined}
              className={cn(
                'flex items-center gap-1 text-xs',
                done && 'text-foreground',
                isCurrent && 'font-medium text-foreground',
                !done && !isCurrent && 'text-muted-foreground',
              )}
            >
              {done && <Check className="size-3.5" aria-hidden />}
              {step}
            </span>
          </li>
        )
      })}
    </ol>
  )
}

function CostSample({ value, spent, tone }: { value: string; spent: number; tone: 'brand' | 'warning' }) {
  return (
    <div className="space-y-1.5">
      <p className="text-sm tabular-nums">{value}</p>
      <Progress value={spent} tone={tone} aria-label={`Budget spent: ${value}`} />
    </div>
  )
}

function MissionHeaderSample() {
  return (
    <div className="space-y-4 rounded-md border border-dashed border-border p-6">
      {/* Sample only: h2 here keeps the showcase page to a single real h1;
          the canonical PageHeader always renders an h1. */}
      <div className="mb-2 flex items-center gap-3">
        <h2 className="text-title font-semibold text-foreground">Weekly GitHub digest</h2>
        <StatusBadge status="working" />
      </div>
      <PhaseStepper current="Build" />
      <div className="max-w-xs space-y-3">
        <CostSample value="$0.0421 of $1.00" spent={4} tone="brand" />
        <CostSample value="$0.86 of $1.00" spent={86} tone="warning" />
      </div>
    </div>
  )
}

function ProviderCardSample() {
  return (
    <div className="grid gap-4 sm:grid-cols-2">
      {providers.map((provider) => {
        const preset = presetForProviderName(provider.name)
        return (
          <Card key={provider.name}>
            <div className="flex items-center justify-between gap-2">
              <span className="flex items-center gap-2 text-sm font-medium">
                {preset && <ProviderLogo preset={preset} className="size-6" />}
                {provider.name}
              </span>
              <StatusBadge status={provider.status} label={provider.statusLabel} />
            </div>
            <p className="mt-2 text-xs text-muted-foreground">Last checked {provider.lastChecked}</p>
            <div className="mt-3">
              <Button variant="test" size="sm">
                Test connection
              </Button>
            </div>
          </Card>
        )
      })}
    </div>
  )
}

export function AgentSurfaces() {
  return (
    <div className="space-y-10">
      <section>
        <SectionHeader title="Conversation" />
        <p className="mb-4 text-sm text-muted-foreground">Evaluate: unchromed Timothy prose against a muted user bubble, recessed trace.</p>
        <ConversationSample approvalTitle="Timothy wants to run a command" />
      </section>

      <section>
        <SectionHeader title="Agent status line" />
        <p className="mb-4 text-sm text-muted-foreground">Evaluate: one line, one icon set, six words, nothing else pulses.</p>
        <AgentStatusLineSample />
      </section>

      <section>
        <SectionHeader title="Approval" />
        <p className="mb-4 text-sm text-muted-foreground">Evaluate: the most formal object on screen, same shape in chat and missions.</p>
        <div className="max-w-[45rem] space-y-3">
          <ApprovalSample title="Timothy wants to run a command (standalone)" />
          <TraceRow status="success" action="Allowed once" target="shell" duration="14:02" />
        </div>
      </section>

      <section>
        <SectionHeader title="Event log" />
        <p className="mb-4 text-sm text-muted-foreground">
          Evaluate: the canonical EventLog component, follow toggle, day divider, the label column, nested tool
          calls (contract 14.11).
        </p>
        <div className="max-w-2xl">
          <EventLogComponentSample />
        </div>
      </section>

      <section>
        <SectionHeader title="Mission header" />
        <p className="mb-4 text-sm text-muted-foreground">Evaluate: comfortable header, neutral phase stepper, cost as text plus a bar.</p>
        <MissionHeaderSample />
      </section>

      <section>
        <SectionHeader title="Provider cards" />
        <p className="mb-4 text-sm text-muted-foreground">Evaluate: status badge with text, the info-toned test action.</p>
        <ProviderCardSample />
      </section>

      <section>
        <SectionHeader title="Side by side" />
        <p className="mb-4 text-sm text-muted-foreground">Evaluate: the calm/operational contrast on one screen (contract 14.1).</p>
        <div className="grid gap-8 lg:grid-cols-[minmax(0,45rem)_minmax(0,1fr)]">
          <ConversationSample approvalTitle="Timothy wants to run a command (side by side)" />
          <EventLogComponentSample />
        </div>
      </section>
    </div>
  )
}
