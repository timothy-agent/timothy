import { useNavigate } from 'react-router'

import type { AutomationRun, AutomationTrigger } from '../../api/types'
import { relativeTime } from '../../lib/format'
import { automationRunStatus } from '../timothy/status'
import { StatusBadge } from '../timothy/status-badge'
import { Badge } from '../ui/badge'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '../ui/table'
import { runDuration, runStatusLabel } from './runs'

const triggerLabel: Record<AutomationTrigger['kind'], string> = {
  cron: 'cron',
  manual: 'manual',
  webhook: 'webhook',
  connector_event: 'connector event',
  channel: 'channel',
}

// RunHistoryTable lists runs newest first; a row with a mission opens it.
export function RunHistoryTable({ runs, triggers }: { runs: AutomationRun[]; triggers: AutomationTrigger[] }) {
  const navigate = useNavigate()
  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>Trigger</TableHead>
          <TableHead>Status</TableHead>
          <TableHead>Started</TableHead>
          <TableHead numeric>Duration</TableHead>
          <TableHead>Skip reason</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {runs.map((r) => {
          const kind = triggers.find((t) => t.id === r.trigger_id)?.kind
          const open = r.mission_id ? () => navigate(`/missions/${r.mission_id}`) : undefined
          const started = r.started_at ?? r.created_at
          return (
            <TableRow
              key={r.id}
              data-run-id={r.id}
              role={open ? 'link' : undefined}
              tabIndex={open ? 0 : undefined}
              aria-label={open ? `Open the mission from the run ${relativeTime(started)}` : undefined}
              className={open ? 'cursor-pointer' : undefined}
              onClick={open}
              onKeyDown={open && ((e) => e.key === 'Enter' && open())}
            >
              <TableCell>
                <Badge variant="outline" size="sm">
                  {kind ? triggerLabel[kind] : 'run now'}
                </Badge>
              </TableCell>
              <TableCell>
                <StatusBadge status={automationRunStatus(r.status)} label={runStatusLabel[r.status]} size="sm" />
              </TableCell>
              <TableCell className="text-xs text-muted-foreground tabular-nums">{relativeTime(started)}</TableCell>
              <TableCell numeric className="text-xs text-muted-foreground">
                {runDuration(r)}
              </TableCell>
              <TableCell className="text-muted-foreground">{r.skip_reason}</TableCell>
            </TableRow>
          )
        })}
      </TableBody>
    </Table>
  )
}
