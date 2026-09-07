import { useState } from 'react'
import { Bot, FileText, Inbox } from 'lucide-react'

import { SectionHeader } from '@/components/timothy/page-header'
import { Panel } from '@/components/timothy/panel'
import { Field, FieldGroup, Form, FormActions } from '@/components/timothy/field'
import { EmptyState } from '@/components/timothy/empty-state'
import { StatusBadge } from '@/components/timothy/status-badge'
import { Combobox } from '@/components/timothy/combobox'
import { useConfirm } from '@/components/timothy/confirm-dialog'
import { CopyButton } from '@/components/timothy/copy-button'
import { JsonBlock } from '@/components/timothy/json-block'
import { Kbd, KbdGroup } from '@/components/timothy/kbd'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { Label } from '@/components/ui/label'
import { Card } from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/components/ui/dialog'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { missionGoal, toolCallArgs } from './fixtures'

const missionCards = [
  { status: 'working' as const, harness: 'Native', title: 'Weekly GitHub digest', phase: 'Generate · 3 of 5', model: 'claude-sonnet-5', time: '2 min ago', cost: '$0.0421' },
  { status: 'waiting' as const, harness: 'Claude Code', title: 'Migrate provider settings to credential_ref', phase: 'Plan · 1 of 4', model: 'gpt-5.2-mini', time: '18 min ago', cost: '$0.0087' },
  { status: 'success' as const, harness: 'Codex', title: 'Summarize unread email from the last 24 hours', phase: 'Result · 5 of 5', model: 'qwen3:4b', time: '1 hour ago', cost: '$0.0000' },
]

const fileRows = [
  { path: 'digest-template.md', size: '2.1 KB' },
  { path: 'internal/brain/missions/driver.go', size: '18.4 KB' },
  { path: 'internal/brain/missions/verifier.go', size: '9.2 KB' },
  { path: 'docs/2026-09-07-digest-notes.md', size: '640 B' },
  { path: 'internal/brain/destinations/email.go', size: '4.8 KB' },
]

export function Compositions() {
  const [confirm, confirmElement] = useConfirm()
  const [confirmResult, setConfirmResult] = useState<string | null>(null)
  const [model, setModel] = useState<string | undefined>('claude-sonnet-5')

  async function handleDeleteMission() {
    const ok = await confirm({
      title: 'Delete mission',
      description: 'This permanently removes the mission and its timeline. Attached artifacts are not affected.',
      confirmLabel: 'Delete',
      destructive: true,
    })
    setConfirmResult(ok ? 'Mission deleted.' : 'Deletion cancelled.')
  }

  return (
    <div className="space-y-10">
      <section>
        <SectionHeader title="Page header" />
        <p className="mb-4 text-sm text-muted-foreground">Evaluate: one brand action per region, meta slot beside the title.</p>
        <div className="rounded-md border border-dashed border-border p-6">
          {/* Sample only: uses h2 here so the showcase page keeps a single
              real h1; the canonical PageHeader always renders an h1. The
              breadcrumb trail is plain text (not a <nav>) so this sample
              doesn't duplicate the page's own breadcrumb landmark. */}
          <div className="mb-2 flex items-center gap-1.5 text-xs text-muted-foreground">
            <span>Missions</span>
            <span aria-hidden>/</span>
            <span className="text-foreground">Weekly GitHub digest</span>
          </div>
          <div className="flex flex-col items-start justify-between gap-4 sm:flex-row">
            <div className="flex items-center gap-3">
              <h2 className="text-title font-semibold text-foreground">Weekly GitHub digest</h2>
              <StatusBadge status="waiting" label="Waiting for approval" />
            </div>
            <div className="flex shrink-0 items-center gap-2">
              <Button variant="outline">Steer</Button>
              <Button variant="ghost">Pause</Button>
              <Button>Approve plan</Button>
            </div>
          </div>
          <p className="mt-1 max-w-2xl text-sm text-muted-foreground">
            Collects unread notifications and emails a Markdown summary every Monday.
          </p>
        </div>
      </section>

      <section>
        <SectionHeader title="Form" />
        <p className="mb-4 text-sm text-muted-foreground">Evaluate: 20px between fields, 32px between fieldsets, right-aligned actions.</p>
        <Form className="max-w-2xl">
          <FieldGroup title="Provider">
            <Field label="Name">{(props) => <Input {...props} defaultValue="OpenAI" />}</Field>
            <Field
              label="API key"
              description="Stored as credential_ref OPENAI_KEY, the value never leaves the server."
            >
              {(props) => <Input {...props} type="password" placeholder="sk-…" />}
            </Field>
            <Field label="Base URL" optional>
              {(props) => <Input {...props} placeholder="https://api.openai.com/v1" />}
            </Field>
            <Field label="Default model">
              {() => (
                <Combobox
                  options={[
                    { value: 'gpt-5.2-mini', label: 'gpt-5.2-mini' },
                    { value: 'claude-sonnet-5', label: 'claude-sonnet-5' },
                  ]}
                  value={model}
                  onChange={setModel}
                  aria-label="Default model"
                />
              )}
            </Field>
            <Field label="Enabled" description="Missions and chat can route to this provider.">
              {(props) => <Switch {...props} defaultChecked />}
            </Field>
          </FieldGroup>
          <FieldGroup title="Limits">
            <Field label="Daily budget">
              {(props) => (
                <div className="relative">
                  <Input {...props} defaultValue="10.00" className="pr-12" />
                  <span className="pointer-events-none absolute inset-y-0 right-3 flex items-center text-xs text-muted-foreground">
                    USD
                  </span>
                </div>
              )}
            </Field>
            <Field label="Sensitivity" htmlFor="sensitivity-local">
              {() => (
                <RadioGroup defaultValue="local">
                  <div className="flex items-center gap-2.5">
                    <RadioGroupItem value="local" id="sensitivity-local" />
                    <Label htmlFor="sensitivity-local">Local</Label>
                  </div>
                  <div className="flex items-center gap-2.5">
                    <RadioGroupItem value="remote" id="sensitivity-remote" />
                    <Label htmlFor="sensitivity-remote">Remote</Label>
                  </div>
                </RadioGroup>
              )}
            </Field>
          </FieldGroup>
          <FormActions destructive={<Button variant="destructive">Delete provider</Button>}>
            <Button variant="outline">Cancel</Button>
            <Button>Save</Button>
          </FormActions>
        </Form>
      </section>

      <section>
        <SectionHeader title="Panel" />
        <p className="mb-4 text-sm text-muted-foreground">Evaluate: comfortable prose panel next to an operational divided list.</p>
        <div className="grid gap-4 lg:grid-cols-2">
          <Panel title="Goal" headingLevel="h3">
            <p className="text-prose">{missionGoal}</p>
          </Panel>
          <Panel title="Files" density="operational" headingLevel="h3">
            <div className="divide-y divide-border">
              {fileRows.map((file) => (
                <div key={file.path} className="flex h-8 items-center gap-2 px-4 text-sm">
                  <FileText className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
                  <span className="truncate font-mono text-xs">{file.path}</span>
                  <span className="ml-auto shrink-0 text-xs text-muted-foreground tabular-nums">{file.size}</span>
                </div>
              ))}
            </div>
          </Panel>
        </div>
      </section>

      <section>
        <SectionHeader title="Mission cards" />
        <p className="mb-4 text-sm text-muted-foreground">Evaluate: title on its own line, metadata row 8px below, one interactive target per card.</p>
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {missionCards.map((card) => (
            <Card key={card.title} interactive asChild>
              <a href="#" aria-label={card.title}>
                <div className="flex items-center justify-between gap-2">
                  <StatusBadge status={card.status} />
                  <span className="flex items-center gap-1 text-xs text-muted-foreground">
                    <Bot className="size-3.5" aria-hidden />
                    {card.harness}
                  </span>
                </div>
                <p className="mt-3 line-clamp-2 text-sm font-medium">{card.title}</p>
                <div className="mt-2 flex flex-wrap items-center gap-x-1.5 gap-y-0.5 text-xs text-muted-foreground">
                  <span className="whitespace-nowrap">{card.phase}</span>
                  <span aria-hidden>&middot;</span>
                  <span className="whitespace-nowrap font-mono">{card.model}</span>
                  <span aria-hidden>&middot;</span>
                  <span className="whitespace-nowrap">{card.time}</span>
                  <span aria-hidden>&middot;</span>
                  <span className="whitespace-nowrap tabular-nums">{card.cost}</span>
                </div>
              </a>
            </Card>
          ))}
        </div>
      </section>

      <section>
        <SectionHeader title="Empty state" />
        <p className="mb-4 text-sm text-muted-foreground">Evaluate: one brand action, description explains what the surface is for.</p>
        <Panel>
          <EmptyState
            icon={Inbox}
            title="No missions yet"
            description="Missions run multi-step work with a plan, verification and review."
            action={<Button>New mission</Button>}
          />
        </Panel>
      </section>

      <section>
        <SectionHeader title="Dialog" />
        <p className="mb-4 text-sm text-muted-foreground">Evaluate: 24px padding, right-aligned footer, confirm dialogs for irreversible actions.</p>
        <div className="flex flex-wrap items-center gap-4">
          <Dialog>
            <DialogTrigger asChild>
              <Button variant="outline">Edit schedule</Button>
            </DialogTrigger>
            <DialogContent>
              <DialogHeader>
                <DialogTitle>Edit schedule</DialogTitle>
                <DialogDescription>Changes apply to the next scheduled run.</DialogDescription>
              </DialogHeader>
              <div className="space-y-5">
                <Field label="Cron expression">{(props) => <Input {...props} defaultValue="0 8 * * 1" className="font-mono" />}</Field>
                <Field label="Timezone" htmlFor="tz-select">
                  {() => (
                    <Select defaultValue="Europe/Amsterdam">
                      <SelectTrigger id="tz-select" className="w-full">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="Europe/Amsterdam">Europe/Amsterdam</SelectItem>
                        <SelectItem value="UTC">UTC</SelectItem>
                      </SelectContent>
                    </Select>
                  )}
                </Field>
              </div>
              <DialogFooter>
                <Button variant="outline">Cancel</Button>
                <Button>Save</Button>
              </DialogFooter>
            </DialogContent>
          </Dialog>

          <Button variant="destructive" onClick={handleDeleteMission}>
            Delete mission
          </Button>
          {confirmResult && <span className="text-sm text-muted-foreground">{confirmResult}</span>}
          {confirmElement}
        </div>
      </section>

      <section>
        <SectionHeader title="Copy button, JsonBlock, Kbd" />
        <p className="mb-4 text-sm text-muted-foreground">Evaluate: trace density truncates long payloads, prose density stays compact.</p>
        <div className="space-y-4">
          <div className="flex items-center gap-2">
            <span className="font-mono text-xs">msn_01J8QK3F7ZC9YV2</span>
            <CopyButton value="msn_01J8QK3F7ZC9YV2" label="Copy mission id" />
          </div>
          <JsonBlock value={toolCallArgs} density="trace" label="Tool call arguments" maxLines={20} />
          <JsonBlock value={{ tool: 'shell', cmd: 'go test ./...' }} density="prose" label="Short payload" />
          <KbdGroup>
            <Kbd>⌘</Kbd>
            <Kbd>K</Kbd>
          </KbdGroup>
        </div>
      </section>
    </div>
  )
}
