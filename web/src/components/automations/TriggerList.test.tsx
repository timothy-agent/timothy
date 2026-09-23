import { fireEvent, render, screen, within } from '@testing-library/react'
import { useState } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { TooltipProvider } from '../ui/tooltip'
import { cronError, draftsFromTriggers, draftsToInput, newTriggerDraft, type TriggerDraft } from './triggerDrafts'
import { TriggerList } from './TriggerList'

function Harness({ initial, onChange }: { initial: TriggerDraft[]; onChange?: (d: TriggerDraft[]) => void }) {
  const [value, setValue] = useState(initial)
  return (
    <TooltipProvider>
      <TriggerList
        value={value}
        onChange={(next) => {
          setValue(next)
          onChange?.(next)
        }}
      />
    </TooltipProvider>
  )
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

  it('offers the Phase 2 kinds disabled', async () => {
    render(<Harness initial={[newTriggerDraft()]} />)
    fireEvent.click(screen.getByLabelText('Kind'))
    const webhook = await screen.findByRole('option', { name: 'Webhook (Phase 2)' })
    expect(webhook).toHaveAttribute('data-disabled')
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
      <TooltipProvider>
        <TriggerList value={[d]} onChange={vi.fn()} errors={{ [d.key]: 'invalid cron expression: bad hour' }} />
      </TooltipProvider>,
    )
    const group = screen.getByRole('group', { name: 'Trigger 1' })
    expect(within(group).getByRole('alert')).toHaveTextContent('invalid cron expression: bad hour')
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
