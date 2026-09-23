import { fireEvent, render, screen, within } from '@testing-library/react'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { describe, expect, it } from 'vitest'
import type { AutomationRun } from '../../api/types'
import { RunHistoryTable } from './RunHistoryTable'
import { isPendingRun, runDuration } from './runs'
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
})
