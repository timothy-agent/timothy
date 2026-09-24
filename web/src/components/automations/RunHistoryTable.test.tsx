import { fireEvent, render, screen, within } from '@testing-library/react'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { describe, expect, it } from 'vitest'
import type { AutomationRun } from '../../api/types'
import { RunHistoryTable } from './RunHistoryTable'
import { isPendingRun, runDuration, runEventLabel } from './runs'
import { makeRun, makeTrigger } from './testFixtures'

function renderTable(runs: AutomationRun[]) {
  const router = createMemoryRouter(
    [
      { path: '/', element: <RunHistoryTable runs={runs} triggers={[makeTrigger(), makeTrigger({ id: 't2', kind: 'manual', config: {} })]} /> },
      { path: '/missions/:id', element: <div>mission page</div> },
    ],
    { initialEntries: ['/'] },
  )
  render(<RouterProvider router={router} />)
  return router
}

const row = (id: string) => document.querySelector(`[data-run-id="${id}"]`) as HTMLElement

describe('RunHistoryTable', () => {
  it('renders a status pill per run, including pending states', () => {
    renderTable([
      makeRun({ id: 'a', status: 'queued', started_at: undefined, finished_at: undefined }),
      makeRun({ id: 'b', status: 'starting', started_at: undefined, finished_at: undefined }),
      makeRun({ id: 'c', status: 'running', finished_at: undefined }),
      makeRun({ id: 'd', status: 'failed' }),
    ])
    expect(within(row('a')).getByText('Queued')).toBeInTheDocument()
    expect(within(row('b')).getByText('Starting')).toBeInTheDocument()
    expect(within(row('c')).getByText('Running').closest('[data-status]')).toHaveAttribute('data-status', 'working')
    expect(within(row('d')).getByText('Failed').closest('[data-status]')).toHaveAttribute('data-status', 'error')
  })

  it('shows the duration only once a run finished', () => {
    renderTable([makeRun({ id: 'a' }), makeRun({ id: 'b', status: 'running', finished_at: undefined })])
    expect(within(row('a')).getByText('2m 30s')).toBeInTheDocument()
    expect(within(row('b')).queryByText(/\d+m \d+s|\d+(\.\d)?s$/)).toBeNull()
  })

  it('shows the skip reason of a skipped run', () => {
    renderTable([makeRun({ id: 'a', status: 'skipped', skip_reason: 'previous run still active', mission_id: undefined })])
    expect(within(row('a')).getByText('Skipped')).toBeInTheDocument()
    expect(within(row('a')).getByText('previous run still active')).toBeInTheDocument()
  })

  it('labels the trigger kind, and run now when no trigger matches', () => {
    renderTable([makeRun({ id: 'a' }), makeRun({ id: 'b', trigger_id: 't2' }), makeRun({ id: 'c', trigger_id: undefined })])
    expect(within(row('a')).getByText('cron')).toBeInTheDocument()
    expect(within(row('b')).getByText('manual')).toBeInTheDocument()
    expect(within(row('c')).getByText('run now')).toBeInTheDocument()
  })

  it('shows the event kind and summary for connector and webhook runs', () => {
    renderTable([
      makeRun({ id: 'a', event: { kind: 'pr.opened', source: 'connector', repo: 'octo/timothy', number: 123 } }),
      makeRun({ id: 'b', event: { kind: 'webhook.received', source: 'webhook', scheme: 'github', delivery: 'abcdef1234567890' } }),
      makeRun({ id: 'c', event: { kind: 'cron.due', source: 'cron', boundary: '2026-07-20T08:00:00Z' } }),
    ])
    expect(within(row('a')).getByText('pr.opened')).toBeInTheDocument()
    expect(within(row('a')).getByText('octo/timothy #123')).toBeInTheDocument()
    expect(within(row('b')).getByText('webhook')).toBeInTheDocument()
    expect(within(row('b')).getByText('abcdef12')).toBeInTheDocument()
    expect(within(row('c')).getByText('cron')).toBeInTheDocument()
  })

  it('shows a channel badge with the sender for channel runs', () => {
    renderTable([makeRun({ id: 'a', event: { kind: 'channel.message', source: 'channel', sender: 'Ada', text: '/run coverage' } })])
    expect(within(row('a')).getByText('channel')).toBeInTheDocument()
    expect(within(row('a')).getByText('Ada')).toBeInTheDocument()
  })

  it('opens the mission on row click only when the run has one', () => {
    const router = renderTable([makeRun({ id: 'a', mission_id: 'm1' }), makeRun({ id: 'b', status: 'skipped' })])
    expect(row('b')).not.toHaveAttribute('role')
    expect(screen.getAllByRole('link')).toHaveLength(1)
    fireEvent.click(row('b'))
    expect(router.state.location.pathname).toBe('/')
    fireEvent.click(row('a'))
    expect(router.state.location.pathname).toBe('/missions/m1')
  })

  it('opens the mission with Enter', () => {
    const router = renderTable([makeRun({ id: 'a', mission_id: 'm1' })])
    fireEvent.keyDown(row('a'), { key: 'Enter' })
    expect(router.state.location.pathname).toBe('/missions/m1')
  })
})

describe('run helpers', () => {
  it('runDuration is blank without both timestamps', () => {
    expect(runDuration(makeRun({ finished_at: undefined }))).toBe('')
    expect(runDuration(makeRun())).toBe('2m 30s')
  })

  it('isPendingRun covers queued, starting and running', () => {
    expect(['queued', 'starting', 'running', 'done', 'failed', 'skipped'].map((status) => isPendingRun(makeRun({ status: status as AutomationRun['status'] })))).toEqual([
      true,
      true,
      true,
      false,
      false,
      false,
    ])
  })

  it('runEventLabel summarizes connector, webhook and channel events only', () => {
    expect(runEventLabel(makeRun({ event: { kind: 'issue.comment', source: 'connector', repo: 'octo/timothy' } }))).toEqual({
      kind: 'issue.comment',
      summary: 'octo/timothy',
    })
    expect(runEventLabel(makeRun({ event: { kind: 'webhook.received', source: 'webhook', delivery: 'd1' } }))).toEqual({ kind: 'webhook', summary: 'd1' })
    expect(runEventLabel(makeRun({ event: { kind: 'channel.message', source: 'channel', sender: 'Ada' } }))).toEqual({ kind: 'channel', summary: 'Ada' })
    expect(runEventLabel(makeRun({ event: { kind: 'run.now', source: 'manual' } }))).toBeUndefined()
    expect(runEventLabel(makeRun({ event: null }))).toBeUndefined()
  })
})
