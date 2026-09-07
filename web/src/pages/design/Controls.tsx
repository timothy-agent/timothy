import { useState } from 'react'
import { Plus, Loader2 } from 'lucide-react'

import { SectionHeader } from '@/components/timothy/page-header'
import { Field } from '@/components/timothy/field'
import { IconButton } from '@/components/timothy/icon-button'
import { Combobox } from '@/components/timothy/combobox'
import { SegmentedControl } from '@/components/timothy/segmented-control'
import { Kbd, KbdGroup } from '@/components/timothy/kbd'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Checkbox } from '@/components/ui/checkbox'
import { Label } from '@/components/ui/label'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { Switch } from '@/components/ui/switch'
import { Badge } from '@/components/ui/badge'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { missionGoal, models } from './fixtures'

function LoadingButton() {
  const [loading, setLoading] = useState(false)
  return (
    <Button
      aria-busy={loading}
      disabled={loading}
      onClick={() => {
        setLoading(true)
        setTimeout(() => setLoading(false), 1500)
      }}
    >
      {loading && <Loader2 className="size-4 animate-spin motion-keep" aria-hidden />}
      Saving
    </Button>
  )
}

export function Controls() {
  const [model, setModel] = useState<string | undefined>('claude-sonnet-5')
  const [modelSm, setModelSm] = useState<string | undefined>('gpt-5.2-mini')
  const [kind, setKind] = useState('light')
  const [range, setRange] = useState('24h')
  const [checkedA, setCheckedA] = useState(false)
  const [switchB, setSwitchB] = useState(true)

  const comboboxOptions = models.map((m) => ({
    value: m.id,
    label: m.id,
    description: `${m.provider} · ${m.pricePerMtok}`,
  }))

  return (
    <div className="space-y-10">
      <section>
        <SectionHeader title="Button" />
        <p className="mb-4 text-sm text-muted-foreground">Evaluate: one brand-filled button per region, quiet outline and ghost.</p>
        <div className="space-y-4">
          <div className="flex flex-wrap items-center gap-4">
            <Button variant="default">Default</Button>
            <Button variant="outline">Outline</Button>
            <Button variant="secondary">Secondary</Button>
            <Button variant="ghost">Ghost</Button>
            <Button variant="test">Test connection</Button>
            <Button variant="destructive">Destructive</Button>
            <Button variant="link">Link</Button>
          </div>
          <div className="flex flex-wrap items-center gap-4">
            <Button variant="outline" size="xs">
              Extra small
            </Button>
            <Button variant="outline" size="sm">
              Small
            </Button>
            <Button variant="outline" size="default">
              Default
            </Button>
            <Button variant="outline" size="lg">
              Large
            </Button>
          </div>
          <div className="flex flex-wrap items-center gap-4">
            <IconButton label="New mission" icon={Plus} size="lg" />
            <IconButton label="New mission" icon={Plus} size="default" />
            <IconButton label="New mission" icon={Plus} size="sm" />
            <IconButton label="New mission" icon={Plus} size="xs" />
            <Button disabled>Disabled</Button>
            <LoadingButton />
            <Button>
              <Plus aria-hidden />
              New mission
            </Button>
          </div>
        </div>
      </section>

      <section>
        <SectionHeader title="Input" />
        <p className="mb-4 text-sm text-muted-foreground">Evaluate: 36px default, 32px operational, error shape.</p>
        <div className="flex flex-wrap items-end gap-4">
          <Input placeholder="sk-…" className="w-56" />
          <Input placeholder="sk-…" size="sm" className="w-56" />
          <Input placeholder="sk-…" disabled className="w-56" />
          <Input disabled defaultValue="sumonmselim@gmail.com" aria-label="Disabled input" className="w-56" />
          <div className="w-56">
            <Field label="API key" error="Key must start with sk-">
              {(props) => <Input {...props} placeholder="sk-…" />}
            </Field>
          </div>
        </div>
      </section>

      <section>
        <SectionHeader title="Select" />
        <p className="mb-4 text-sm text-muted-foreground">Evaluate: identifiers rendered mono, control heights match Input.</p>
        <div className="flex flex-wrap items-center gap-4">
          <Select defaultValue="gpt-5.2-mini">
            <SelectTrigger className="w-56" aria-label="Model">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {models.map((m) => (
                <SelectItem key={m.id} value={m.id}>
                  <span className="font-mono">{m.id}</span>
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Select defaultValue="claude-sonnet-5">
            <SelectTrigger size="sm" className="w-56" aria-label="Model (sm)">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {models.map((m) => (
                <SelectItem key={m.id} value={m.id}>
                  <span className="font-mono">{m.id}</span>
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </section>

      <section>
        <SectionHeader title="Textarea" />
        <p className="mb-4 text-sm text-muted-foreground">Evaluate: prose line rhythm, grows without a resize handle.</p>
        <Field label="Goal">{(props) => <Textarea {...props} defaultValue={missionGoal} className="max-w-2xl" />}</Field>
      </section>

      <section>
        <SectionHeader title="Checkbox" />
        <p className="mb-4 text-sm text-muted-foreground">Evaluate: checked state uses brand, 12px between stacked items.</p>
        <div className="flex flex-wrap items-center gap-6">
          <div className="flex items-center gap-2.5">
            <Checkbox id="cb-unchecked" checked={checkedA} onCheckedChange={(v) => setCheckedA(v === true)} />
            <Label htmlFor="cb-unchecked">Unchecked</Label>
          </div>
          <div className="flex items-center gap-2.5">
            <Checkbox id="cb-checked" checked />
            <Label htmlFor="cb-checked">Checked</Label>
          </div>
          <div className="flex items-center gap-2.5">
            <Checkbox id="cb-indeterminate" checked="indeterminate" />
            <Label htmlFor="cb-indeterminate">Indeterminate</Label>
          </div>
          <div className="flex items-center gap-2.5">
            <Checkbox id="cb-disabled" disabled />
            <Label htmlFor="cb-disabled">Disabled</Label>
          </div>
        </div>
        <div className="mt-4 space-y-3">
          <div className="flex items-center gap-2.5">
            <Checkbox id="cb-email" defaultChecked />
            <Label htmlFor="cb-email">Email</Label>
          </div>
          <div className="flex items-center gap-2.5">
            <Checkbox id="cb-telegram" />
            <Label htmlFor="cb-telegram">Telegram</Label>
          </div>
          <div className="flex items-center gap-2.5">
            <Checkbox id="cb-webhook" />
            <Label htmlFor="cb-webhook">Webhook</Label>
          </div>
        </div>
      </section>

      <section>
        <SectionHeader title="Radio" />
        <p className="mb-4 text-sm text-muted-foreground">Evaluate: mutually exclusive options with room for a description.</p>
        <RadioGroup defaultValue="native" className="max-w-md">
          <div className="flex items-start gap-2.5">
            <RadioGroupItem value="native" id="harness-native" className="mt-0.5" />
            <div>
              <Label htmlFor="harness-native">Native</Label>
              <p className="text-sm text-muted-foreground">Runs the mission loop directly in the sandbox.</p>
            </div>
          </div>
          <div className="flex items-start gap-2.5">
            <RadioGroupItem value="claude-code" id="harness-claude" className="mt-0.5" />
            <div>
              <Label htmlFor="harness-claude">Claude Code</Label>
              <p className="text-sm text-muted-foreground">Delegates the turn to the Claude Code CLI.</p>
            </div>
          </div>
          <div className="flex items-start gap-2.5">
            <RadioGroupItem value="codex" id="harness-codex" className="mt-0.5" />
            <div>
              <Label htmlFor="harness-codex">Codex</Label>
              <p className="text-sm text-muted-foreground">Delegates the turn to the Codex CLI.</p>
            </div>
          </div>
        </RadioGroup>
        <RadioGroup defaultValue="native" className="mt-6 max-w-md">
          <label className="flex cursor-pointer items-start gap-3 rounded-md border border-input bg-card p-4 transition-[border-color,box-shadow] duration-100 ease-out has-[[data-state=checked]]:border-brand has-[[data-state=checked]]:ring-1 has-[[data-state=checked]]:ring-brand">
            <RadioGroupItem value="native" id="harness-card-native" className="mt-0.5" />
            <div>
              <p className="text-sm font-medium text-foreground">Native runner</p>
              <p className="text-sm text-muted-foreground">Runs the mission loop directly in the sandbox.</p>
            </div>
          </label>
          <label className="flex cursor-pointer items-start gap-3 rounded-md border border-input bg-card p-4 transition-[border-color,box-shadow] duration-100 ease-out has-[[data-state=checked]]:border-brand has-[[data-state=checked]]:ring-1 has-[[data-state=checked]]:ring-brand">
            <RadioGroupItem value="claude-code-card" id="harness-card-claude" className="mt-0.5" />
            <div>
              <p className="text-sm font-medium text-foreground">Delegated CLI (Claude Code)</p>
              <p className="text-sm text-muted-foreground">Delegates the turn to the Claude Code CLI.</p>
            </div>
          </label>
        </RadioGroup>
      </section>

      <section>
        <SectionHeader title="Switch" />
        <p className="mb-4 text-sm text-muted-foreground">Evaluate: switches commit immediately, brand only when checked.</p>
        <div className="flex flex-wrap items-center gap-6">
          <div className="flex items-center gap-2.5">
            <Switch id="sw-off" checked={false} />
            <Label htmlFor="sw-off">Off</Label>
          </div>
          <div className="flex items-center gap-2.5">
            <Switch id="sw-on" checked={switchB} onCheckedChange={setSwitchB} />
            <Label htmlFor="sw-on">On</Label>
          </div>
          <div className="flex items-center gap-2.5">
            <Switch id="sw-disabled" disabled />
            <Label htmlFor="sw-disabled">Disabled</Label>
          </div>
        </div>
        <p className="mt-2 text-xs text-muted-foreground">Switches commit immediately.</p>
      </section>

      <section>
        <SectionHeader title="Badge" />
        <p className="mb-4 text-sm text-muted-foreground">Evaluate: soft background paired with its own foreground, pill radius.</p>
        <div className="flex flex-wrap items-center gap-3">
          <Badge variant="neutral">Neutral</Badge>
          <Badge variant="brand">Brand</Badge>
          <Badge variant="good">Good</Badge>
          <Badge variant="warning">Warning</Badge>
          <Badge variant="info">Info</Badge>
          <Badge variant="destructive">Destructive</Badge>
          <Badge variant="outline">Outline</Badge>
        </div>
        <div className="mt-3 flex flex-wrap items-center gap-3">
          <Badge variant="neutral" size="sm">
            Neutral
          </Badge>
          <Badge variant="brand" size="sm">
            Brand
          </Badge>
          <Badge variant="good" size="sm">
            Good
          </Badge>
        </div>
      </section>

      <section>
        <SectionHeader title="Tabs" />
        <p className="mb-4 text-sm text-muted-foreground">Evaluate: local view switch, never used for top-level navigation.</p>
        <Tabs defaultValue="timeline" className="max-w-md">
          <TabsList>
            <TabsTrigger value="timeline">Timeline</TabsTrigger>
            <TabsTrigger value="files">Files</TabsTrigger>
            <TabsTrigger value="result">Result</TabsTrigger>
          </TabsList>
          <TabsContent value="timeline" className="text-sm text-muted-foreground">
            10 events, last one 2 minutes ago.
          </TabsContent>
          <TabsContent value="files" className="text-sm text-muted-foreground">
            3 files changed in the worktree.
          </TabsContent>
          <TabsContent value="result" className="text-sm text-muted-foreground">
            Digest delivered to sumon@cielara.ai.
          </TabsContent>
        </Tabs>
      </section>

      <section>
        <SectionHeader title="Combobox" />
        <p className="mb-4 text-sm text-muted-foreground">Evaluate: mono identifier as the label, provider and price as description.</p>
        <div className="flex flex-wrap items-start gap-4">
          <div className="w-80">
            <Combobox options={comboboxOptions} value={model} onChange={setModel} placeholder="Select model" aria-label="Model" />
          </div>
          <div className="w-80">
            <Combobox
              options={comboboxOptions}
              value={modelSm}
              onChange={setModelSm}
              size="sm"
              placeholder="Select model"
              aria-label="Model (sm)"
            />
          </div>
        </div>
      </section>

      <section>
        <SectionHeader title="Segmented control" />
        <p className="mb-4 text-sm text-muted-foreground">Evaluate: selected segment uses the same brand-soft treatment as navigation.</p>
        <div className="flex flex-wrap items-center gap-6">
          <SegmentedControl
            value={kind}
            onChange={setKind}
            aria-label="Mission kind"
            options={[
              { value: 'light', label: 'Light' },
              { value: 'full', label: 'Full' },
            ]}
          />
          <SegmentedControl
            value={range}
            onChange={setRange}
            size="sm"
            aria-label="Time range"
            options={[
              { value: '24h', label: '24h' },
              { value: '7d', label: '7d' },
              { value: '30d', label: '30d' },
            ]}
          />
        </div>
      </section>

      <section>
        <SectionHeader title="Tooltip and Kbd" />
        <p className="mb-4 text-sm text-muted-foreground">Evaluate: icon-only controls carry a label, shortcut chips read at a glance.</p>
        <div className="flex flex-wrap items-center gap-6">
          <Tooltip>
            <TooltipTrigger asChild>
              <IconButton label="Copy id" icon={Plus} tooltip={false} />
            </TooltipTrigger>
            <TooltipContent>Copy id</TooltipContent>
          </Tooltip>
          <KbdGroup>
            <Kbd>⌘</Kbd>
            <Kbd>K</Kbd>
          </KbdGroup>
        </div>
      </section>

      <section>
        <SectionHeader title="Focus" />
        <p className="mb-4 text-sm text-muted-foreground">Evaluate: one visible ring, brand-coloured, on every focusable element.</p>
        <p className="mb-4 text-sm text-muted-foreground">Tab through this row to see the ring.</p>
        <div className="flex flex-wrap items-center gap-4">
          <Button variant="outline">Focusable button</Button>
          <Input placeholder="Focusable input" className="w-48" />
          <div className="flex items-center gap-2.5">
            <Checkbox id="focus-cb" />
            <Label htmlFor="focus-cb">Checkbox</Label>
          </div>
          <div className="flex items-center gap-2.5">
            <Switch id="focus-sw" />
            <Label htmlFor="focus-sw">Switch</Label>
          </div>
        </div>
      </section>
    </div>
  )
}
