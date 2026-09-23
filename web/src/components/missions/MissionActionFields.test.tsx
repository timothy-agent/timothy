import { fireEvent, render, screen } from '@testing-library/react'
import { useState } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { Destination, MissionTemplate } from '../../api/types'
import { MissionActionFields } from './MissionActionFields'
import { normalizeTemplate } from './missionTemplate'

vi.mock('../../api/client', () => ({ uploadAttachment: vi.fn() }))

const destination = (over: Partial<Destination>): Destination => ({
  id: 'd1',
  name: 'ops-inbox',
  kind: 'email',
  config: {},
  credential_ref: '',
  enabled: true,
  created_at: '2026-07-01T00:00:00Z',
  updated_at: '2026-07-01T00:00:00Z',
  ...over,
})

function Harness({ initial, destinations = [], onChange }: { initial: MissionTemplate; destinations?: Destination[]; onChange?: (t: MissionTemplate) => void }) {
  const [value, setValue] = useState(initial)
  return (
    <MissionActionFields
      value={value}
      onChange={(t) => {
        setValue(t)
        onChange?.(t)
      }}
      routes={[]}
      destinations={destinations}
      attachments={[]}
      onAttachmentsChange={vi.fn()}
    />
  )
}

const last = (fn: ReturnType<typeof vi.fn>) => fn.mock.calls[fn.mock.calls.length - 1][0] as MissionTemplate

beforeEach(() => {
  Element.prototype.scrollIntoView = vi.fn()
})

describe('MissionActionFields', () => {
  it('shows the trigger data hint under the goal', () => {
    render(<Harness initial={{ goal: '', kind: 'general' }} />)
    expect(screen.getByText(/Use \{\{event\.pr_url\}\} or \{\{notes\.name\}\}/)).toBeInTheDocument()
  })

  it('shows harness and environment only for coding, light only for general', () => {
    const onChange = vi.fn()
    render(<Harness initial={{ goal: 'g', kind: 'general', light: true }} onChange={onChange} />)
    expect(screen.getByLabelText('Light mission')).toBeInTheDocument()
    expect(screen.queryByLabelText(/^Harness/)).toBeNull()
    fireEvent.click(screen.getByRole('radio', { name: 'Coding' }))
    expect(last(onChange)).toMatchObject({ kind: 'coding', light: undefined })
    expect(screen.queryByLabelText('Light mission')).toBeNull()
    expect(screen.getByLabelText(/^Harness/)).toBeInTheDocument()
    expect(screen.getByLabelText(/^Environment/)).toBeInTheDocument()
  })

  it('toggles a destination on and off', () => {
    const onChange = vi.fn()
    render(<Harness initial={{ goal: 'g', kind: 'general' }} destinations={[destination({})]} onChange={onChange} />)
    fireEvent.click(screen.getByLabelText('ops-inbox'))
    expect(last(onChange).destination_ids).toEqual(['d1'])
    fireEvent.click(screen.getByLabelText('ops-inbox'))
    expect(last(onChange).destination_ids).toEqual([])
  })

  it('hides a disabled destination unless the template already holds it', () => {
    const { unmount } = render(<Harness initial={{ goal: 'g', kind: 'general' }} destinations={[destination({ enabled: false })]} />)
    expect(screen.queryByLabelText('ops-inbox')).toBeNull()
    unmount()
    render(<Harness initial={{ goal: 'g', kind: 'general', destination_ids: ['d1'] }} destinations={[destination({ enabled: false })]} />)
    expect(screen.getByLabelText('ops-inbox')).toBeInTheDocument()
    expect(screen.getByText(/disabled, enable it in Settings/)).toBeInTheDocument()
  })

  it('reads numbers from max iterations and budget', () => {
    const onChange = vi.fn()
    render(<Harness initial={{ goal: 'g', kind: 'general' }} onChange={onChange} />)
    fireEvent.change(screen.getByLabelText(/^Max iterations/), { target: { value: '4' } })
    expect(last(onChange).max_iterations).toBe(4)
    fireEvent.change(screen.getByLabelText(/^Budget per run/), { target: { value: '1.5' } })
    expect(last(onChange).budget_amount).toBe(1.5)
    fireEvent.change(screen.getByLabelText(/^Budget per run/), { target: { value: '' } })
    expect(last(onChange).budget_amount).toBeUndefined()
  })
})

describe('normalizeTemplate', () => {
  it('drops empty strings and fields the kind does not use', () => {
    expect(
      normalizeTemplate({
        goal: '  g  ',
        kind: 'general',
        route: '',
        harness: 'claude-cli',
        environment: 'go',
        light: true,
        review_harness: 'pi',
        destination_ids: [],
        attachments: [],
      }),
    ).toEqual({ goal: 'g', kind: 'general', auto_approve_tools: true, light: true })
  })

  it('keeps coding fields and a priced budget', () => {
    expect(
      normalizeTemplate({ goal: 'g', kind: 'coding', harness: 'codex-cli', environment: 'go', light: true, budget_amount: 2 }),
    ).toEqual({ goal: 'g', kind: 'coding', harness: 'codex-cli', environment: 'go', auto_approve_tools: true, budget_amount: 2, budget_currency: 'USD' })
  })

  it('keeps an explicit auto-approve off and a template name', () => {
    expect(normalizeTemplate({ goal: 'g', name: 'PR review', kind: 'general', auto_approve_tools: false })).toMatchObject({
      name: 'PR review',
      auto_approve_tools: false,
    })
  })
})
