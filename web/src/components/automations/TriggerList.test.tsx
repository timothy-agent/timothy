import { fireEvent, render, screen, within } from '@testing-library/react'
import { useState } from 'react'
import { MemoryRouter } from 'react-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdminConnector } from '../../api/types'
import { TooltipProvider } from '../ui/tooltip'
import { cronError, draftsFromTriggers, draftsToInput, newTriggerDraft, type TriggerDraft } from './triggerDrafts'
import { TriggerList } from './TriggerList'

const connectorID = '0000000c-0000-0000-0000-000000000001'

const connector = (over: Partial<AdminConnector>): AdminConnector => ({
  id: connectorID,
  name: 'gh-main',
  kind: 'github',
  config: {},
  credential_ref: 'GH_PAT',
  enabled: true,
  sensitive: false,
  ...over,
})

function Harness({
  initial,
  onChange,
  connectors = [],
  submitted,
}: {
  initial: TriggerDraft[]
  onChange?: (d: TriggerDraft[]) => void
  connectors?: AdminConnector[] | null
  submitted?: boolean
}) {
  const [value, setValue] = useState(initial)
  return (
    <MemoryRouter>
      <TooltipProvider>
        <TriggerList
          value={value}
          connectors={connectors}
          submitted={submitted}
          onChange={(next) => {
            setValue(next)
            onChange?.(next)
          }}
        />
      </TooltipProvider>
    </MemoryRouter>
  )
}

async function pickKind(name: string) {
  fireEvent.click(screen.getByLabelText('Kind'))
  fireEvent.click(await screen.findByRole('option', { name }))
}

function addChips(label: string, values: string[]) {
  const input = screen.getByLabelText(label)
  for (const v of values) {
    fireEvent.change(input, { target: { value: v } })
    fireEvent.keyDown(input, { key: 'Enter' })
  }
}

const lastCall = (fn: ReturnType<typeof vi.fn>) => fn.mock.calls[fn.mock.calls.length - 1][0] as TriggerDraft[]

describe('TriggerList', () => {
  beforeEach(() => {
    Element.prototype.scrollIntoView = vi.fn()
  })

  it('adds a trigger and stops at five', () => {
    const onChange = vi.fn()
    render(<Harness initial={[newTriggerDraft()]} onChange={onChange} />)
    const add = screen.getByRole('button', { name: 'Add trigger' })
    for (let i = 0; i < 4; i++) fireEvent.click(add)
    expect(screen.getAllByRole('group')).toHaveLength(5)
    expect(add).toBeDisabled()
    expect(screen.getByText('5 of 5')).toBeInTheDocument()
  })

  it('removes a trigger but never the last one', () => {
    render(<Harness initial={[newTriggerDraft(), newTriggerDraft()]} />)
    fireEvent.click(screen.getByRole('button', { name: 'Remove trigger 2' }))
    expect(screen.getAllByRole('group')).toHaveLength(1)
    expect(screen.getByRole('button', { name: 'Remove trigger 1' })).toBeDisabled()
  })

  it('switches a trigger to manual and hides the cron fields', async () => {
    const onChange = vi.fn()
    render(<Harness initial={[newTriggerDraft()]} onChange={onChange} />)
    fireEvent.click(screen.getByLabelText('Kind'))
    fireEvent.click(await screen.findByRole('option', { name: 'Manual' }))
    expect(lastCall(onChange)[0].kind).toBe('manual')
    expect(screen.queryByLabelText('Cron expression')).toBeNull()
    expect(screen.getByText('Runs only when you press Run now.')).toBeInTheDocument()
  })

  it('offers connector event and webhook, and channel disabled', async () => {
    render(<Harness initial={[newTriggerDraft()]} />)
    fireEvent.click(screen.getByLabelText('Kind'))
    expect(await screen.findByRole('option', { name: 'Webhook' })).not.toHaveAttribute('data-disabled')
    expect(screen.getByRole('option', { name: 'Connector event' })).not.toHaveAttribute('data-disabled')
    expect(screen.getByRole('option', { name: 'Channel message (Phase 3)' })).toHaveAttribute('data-disabled')
  })

  it('writes the picked preset into the expression', async () => {
    const onChange = vi.fn()
    render(<Harness initial={[newTriggerDraft()]} onChange={onChange} />)
    fireEvent.click(screen.getByLabelText('Repeats'))
    fireEvent.click(await screen.findByRole('option', { name: 'Hourly' }))
    expect(screen.getByLabelText('Cron expression')).toHaveValue('0 * * * *')
    expect(lastCall(onChange)[0].expr).toBe('0 * * * *')
  })

  it('shows Custom for a typed expression and flags a bad shape', () => {
    render(<Harness initial={[newTriggerDraft()]} />)
    fireEvent.change(screen.getByLabelText('Cron expression'), { target: { value: '*/5 * * * *' } })
    expect(screen.getByLabelText('Repeats')).toHaveTextContent('Custom')
    fireEvent.change(screen.getByLabelText('Cron expression'), { target: { value: 'every day' } })
    expect(screen.getByRole('alert')).toHaveTextContent('Use five fields')
  })

  it('toggles one trigger off', () => {
    const onChange = vi.fn()
    render(<Harness initial={[newTriggerDraft()]} onChange={onChange} />)
    fireEvent.click(screen.getByRole('switch', { name: 'Trigger 1 enabled' }))
    expect(lastCall(onChange)[0].enabled).toBe(false)
  })

  it('keeps the server id on an edited row', () => {
    const onChange = vi.fn()
    const drafts = draftsFromTriggers([{ id: 't9', kind: 'cron', config: { expr: '0 7 * * *' }, enabled: true }])
    render(<Harness initial={drafts} onChange={onChange} />)
    fireEvent.change(screen.getByLabelText('Cron expression'), { target: { value: '0 9 * * *' } })
    expect(lastCall(onChange)[0]).toMatchObject({ id: 't9', expr: '0 9 * * *' })
  })

  it('shows a server error under its trigger', () => {
    const d = newTriggerDraft()
    render(
      <MemoryRouter>
        <TooltipProvider>
          <TriggerList value={[d]} onChange={vi.fn()} errors={{ [d.key]: 'invalid cron expression: bad hour' }} />
        </TooltipProvider>
      </MemoryRouter>,
    )
    const group = screen.getByRole('group', { name: 'Trigger 1' })
    expect(within(group).getByRole('alert')).toHaveTextContent('invalid cron expression: bad hour')
  })
})

describe('TriggerList connector event', () => {
  beforeEach(() => {
    Element.prototype.scrollIntoView = vi.fn()
  })

  it('lists only enabled github connectors and builds the draft', async () => {
    const onChange = vi.fn()
    const connectors = [
      connector({}),
      connector({ id: 'off', name: 'gh-off', enabled: false }),
      connector({ id: 'mail', name: 'mailbox', kind: 'google' }),
    ]
    render(<Harness initial={[newTriggerDraft()]} onChange={onChange} connectors={connectors} />)
    await pickKind('Connector event')
    fireEvent.click(screen.getByLabelText('Connector'))
    expect(await screen.findByRole('option', { name: 'gh-main' })).toBeInTheDocument()
    expect(screen.queryByRole('option', { name: 'gh-off' })).toBeNull()
    expect(screen.queryByRole('option', { name: 'mailbox' })).toBeNull()
    fireEvent.click(screen.getByRole('option', { name: 'gh-main' }))
    fireEvent.change(screen.getByLabelText('Repository'), { target: { value: 'octo/timothy' } })
    fireEvent.click(screen.getByRole('checkbox', { name: /pr\.opened/ }))
    fireEvent.click(screen.getByRole('checkbox', { name: /pr\.labeled/ }))
    fireEvent.click(screen.getByRole('checkbox', { name: /pr\.opened/ }))
    addChips('Labels optional', ['needs-review'])
    expect(lastCall(onChange)[0]).toMatchObject({
      kind: 'connector_event',
      connectorId: connectorID,
      repo: 'octo/timothy',
      events: ['pr.labeled'],
      labels: ['needs-review'],
    })
  })

  it('flags a repo that is not owner/name', async () => {
    render(<Harness initial={[newTriggerDraft()]} connectors={[connector({})]} />)
    await pickKind('Connector event')
    fireEvent.change(screen.getByLabelText('Repository'), { target: { value: 'timothy' } })
    expect(screen.getByRole('alert')).toHaveTextContent('owner/name')
  })

  it('caps labels at ten and removes one', async () => {
    const onChange = vi.fn()
    render(<Harness initial={[newTriggerDraft()]} onChange={onChange} connectors={[connector({})]} />)
    await pickKind('Connector event')
    addChips('Labels optional', Array.from({ length: 10 }, (_, i) => `l${i}`))
    expect(screen.getByLabelText('Labels optional')).toBeDisabled()
    expect(screen.getByText('10 of 10')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Remove label l3' }))
    expect(lastCall(onChange)[0].labels).toHaveLength(9)
    expect(lastCall(onChange)[0].labels).not.toContain('l3')
  })

  it('points to the connectors page without a github connector', async () => {
    render(<Harness initial={[newTriggerDraft()]} connectors={[connector({ kind: 'google' })]} />)
    await pickKind('Connector event')
    expect(screen.getByRole('link', { name: 'Add a GitHub connector first' })).toHaveAttribute('href', '/settings/connectors')
    expect(screen.queryByLabelText('Repository')).toBeNull()
  })

  it('shows what is missing after a submit', async () => {
    render(<Harness initial={[{ ...newTriggerDraft(), kind: 'connector_event' }]} connectors={[connector({})]} submitted />)
    expect(screen.getByRole('alert')).toHaveTextContent('Pick a GitHub connector.')
  })
})

describe('TriggerList webhook', () => {
  beforeEach(() => {
    Element.prototype.scrollIntoView = vi.fn()
  })

  it('sets scheme and secret name', async () => {
    const onChange = vi.fn()
    render(<Harness initial={[newTriggerDraft()]} onChange={onChange} />)
    await pickKind('Webhook')
    expect(screen.getByRole('radio', { name: 'GitHub' })).toHaveAttribute('data-state', 'on')
    fireEvent.click(screen.getByRole('radio', { name: 'Generic' }))
    fireEvent.change(screen.getByLabelText('Signing secret'), { target: { value: 'HOOK_KEY' } })
    expect(lastCall(onChange)[0]).toMatchObject({ kind: 'webhook', scheme: 'generic', credentialRef: 'HOOK_KEY' })
  })

  it('adds, edits, removes and caps filter rows', async () => {
    const onChange = vi.fn()
    render(<Harness initial={[newTriggerDraft()]} onChange={onChange} />)
    await pickKind('Webhook')
    const add = screen.getByRole('button', { name: 'Add filter' })
    fireEvent.click(add)
    fireEvent.change(screen.getByLabelText('Filter 1 path'), { target: { value: 'action' } })
    fireEvent.change(screen.getByLabelText('Filter 1 equals'), { target: { value: 'opened' } })
    expect(lastCall(onChange)[0].filters).toEqual([{ path: 'action', equals: 'opened' }])
    for (let i = 0; i < 9; i++) fireEvent.click(add)
    expect(add).toBeDisabled()
    expect(screen.getByText('10 of 10')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Remove filter 1' }))
    expect(lastCall(onChange)[0].filters).toHaveLength(9)
    expect(add).toBeEnabled()
  })

  it('flags a bad filter path', async () => {
    render(<Harness initial={[newTriggerDraft()]} />)
    await pickKind('Webhook')
    fireEvent.click(screen.getByRole('button', { name: 'Add filter' }))
    fireEvent.change(screen.getByLabelText('Filter 1 path'), { target: { value: 'a b' } })
    expect(screen.getByRole('alert')).toHaveTextContent('dotted path')
  })

  it('asks for the secret name after a submit', () => {
    render(<Harness initial={[{ ...newTriggerDraft(), kind: 'webhook' }]} submitted />)
    expect(screen.getByRole('alert')).toHaveTextContent('Enter the signing secret name.')
  })
})

describe('TriggerList tool allowlist', () => {
  it('opens collapsed, adds tools split on spaces and commas', () => {
    const onChange = vi.fn()
    render(<Harness initial={[newTriggerDraft()]} onChange={onChange} />)
    expect(screen.queryByLabelText('Tools optional')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Tool allowlist, trigger 1' }))
    const input = screen.getByLabelText('Tools optional')
    fireEvent.change(input, { target: { value: 'search_mail, read_mail  search_mail' } })
    fireEvent.blur(input)
    expect(lastCall(onChange)[0].toolAllowlist).toEqual(['search_mail', 'read_mail'])
  })

  it('starts open when the trigger has an allowlist', () => {
    render(<Harness initial={[{ ...newTriggerDraft(), toolAllowlist: ['search_mail'] }]} />)
    expect(screen.getByText('search_mail')).toBeInTheDocument()
    expect(screen.getByText('1 of 64')).toBeInTheDocument()
  })
})

describe('draftsToInput', () => {
  it('keeps ids and allowlists, sends manual with empty config', () => {
    const drafts = draftsFromTriggers([
      { id: 't1', kind: 'cron', config: { expr: ' 0 7 * * * ' }, enabled: true, tool_allowlist: ['search_mail'] },
      { kind: 'manual', config: {} },
    ])
    expect(draftsToInput(drafts)).toEqual([
      { id: 't1', kind: 'cron', config: { expr: '0 7 * * *' }, enabled: true, tool_allowlist: ['search_mail'] },
      { kind: 'manual', config: {}, enabled: true },
    ])
  })
})

describe('cronError', () => {
  it('only checks cron drafts', () => {
    expect(cronError({ ...newTriggerDraft(), expr: 'nope' })).toBeTruthy()
    expect(cronError({ ...newTriggerDraft(), kind: 'manual', expr: '' })).toBeUndefined()
    expect(cronError(newTriggerDraft())).toBeUndefined()
  })
})
