import { useCallback, useEffect, useState } from 'react'
import { toast } from 'sonner'
import { approveChannelPairing, listChannelPairings, revokeChannelPairing } from '../../api/client'
import type { ChannelPairing } from '../../api/types'
import { Badge } from '../ui/badge'
import { Button } from '../ui/button'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '../ui/table'
import { EmptyState } from '../timothy/empty-state'
import { Panel } from '../timothy/panel'
import type { Status } from '../timothy/status'
import { StatusBadge } from '../timothy/status-badge'
import { errText } from '../../lib/errors'

// pollEvery refreshes pairings while the page is open.
const pollEvery = 10_000

const pairingStatus: Record<ChannelPairing['status'], { status: Status; label: string }> = {
  pending: { status: 'waiting', label: 'Pending' },
  approved: { status: 'success', label: 'Approved' },
  revoked: { status: 'neutral', label: 'Revoked' },
}

// expiresIn renders a code's remaining lifetime as m:ss, or "expired".
function expiresIn(expiresAt: string | undefined, now: number): string {
  if (!expiresAt) return ''
  const left = Math.floor((Date.parse(expiresAt) - now) / 1000)
  if (left <= 0) return 'expired'
  return `expires in ${Math.floor(left / 60)}:${String(left % 60).padStart(2, '0')}`
}

// ChannelPairings lists a channel's senders, pending first, with the
// pairing code for pending ones and approve/revoke actions.
export function ChannelPairings({ channelID }: { channelID: string }) {
  const [pairings, setPairings] = useState<ChannelPairing[] | null>(null)
  const [now, setNow] = useState(() => Date.now())
  const [busy, setBusy] = useState<string | null>(null)

  const refresh = useCallback(() => {
    listChannelPairings(channelID)
      .then(setPairings)
      .catch((err: unknown) => toast.error('Could not load pairings', { description: errText(err) }))
  }, [channelID])

  useEffect(() => {
    refresh()
    const poll = setInterval(refresh, pollEvery)
    const clock = setInterval(() => setNow(Date.now()), 1000)
    return () => {
      clearInterval(poll)
      clearInterval(clock)
    }
  }, [refresh])

  const decide = async (p: ChannelPairing, action: 'approve' | 'revoke') => {
    setBusy(p.external_user_id)
    try {
      if (action === 'approve') await approveChannelPairing(channelID, p.external_user_id)
      else await revokeChannelPairing(channelID, p.external_user_id)
      toast.success(action === 'approve' ? `${p.display_name} approved` : `${p.display_name} revoked`)
      refresh()
    } catch (err) {
      toast.error(`Could not ${action} ${p.display_name}`, { description: errText(err) })
    } finally {
      setBusy(null)
    }
  }

  return (
    <Panel
      title="Pairings"
      description="People who messaged this bot. Only approved senders reach a model."
      density="operational"
    >
      {pairings && pairings.length === 0 ? (
        <EmptyState density="operational" title="No one has messaged this bot yet" />
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Telegram id</TableHead>
              <TableHead>Status</TableHead>
              <TableHead>Code</TableHead>
              <TableHead>
                <span className="sr-only">Actions</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {(pairings ?? []).map((p) => {
              const meta = pairingStatus[p.status]
              return (
                <TableRow key={p.external_user_id}>
                  <TableCell>{p.display_name}</TableCell>
                  <TableCell className="font-mono text-xs">{p.external_user_id}</TableCell>
                  <TableCell>
                    <StatusBadge status={meta.status} label={meta.label} size="sm" />
                  </TableCell>
                  <TableCell>
                    {p.status === 'pending' && p.code ? (
                      <div className="flex items-center gap-2">
                        <Badge variant="outline" className="font-mono">
                          {p.code}
                        </Badge>
                        <span className="text-xs text-muted-foreground tabular-nums">{expiresIn(p.code_expires_at, now)}</span>
                      </div>
                    ) : null}
                  </TableCell>
                  <TableCell>
                    <div className="flex justify-end gap-2">
                      {p.status !== 'approved' && (
                        <Button size="sm" disabled={busy === p.external_user_id} onClick={() => void decide(p, 'approve')}>
                          Approve
                        </Button>
                      )}
                      {p.status !== 'revoked' && (
                        <Button
                          size="sm"
                          variant="outline"
                          disabled={busy === p.external_user_id}
                          onClick={() => void decide(p, 'revoke')}
                        >
                          Revoke
                        </Button>
                      )}
                    </div>
                  </TableCell>
                </TableRow>
              )
            })}
          </TableBody>
        </Table>
      )}
    </Panel>
  )
}
