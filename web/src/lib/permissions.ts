import { useEffect, useState } from 'react'
import { listPendingPermissions } from '../api/client'
import type { PendingPermission } from '../api/types'
import { subscribeEvents } from './events'

// usePendingPermissions keeps the global badge/toast current: initial
// fetch, a slow poll as a safety net for a missed signal, and an
// instant refetch on the "permission" hub signal (fired on both
// request and resolve — see chat.Service.SetPermissionHub). Returns
// the full list, not just a count: the toast needs each entry's tool
// name and session id to build its message and jump link.
export function usePendingPermissions(): PendingPermission[] {
  const [pending, setPending] = useState<PendingPermission[]>([])

  useEffect(() => {
    let live = true
    const refresh = () => {
      listPendingPermissions()
        .then((p) => live && setPending(p))
        .catch(() => {})
    }
    refresh()
    const timer = setInterval(refresh, 60_000)
    const unsubscribe = subscribeEvents((sig) => {
      if (sig.kind === 'permission') refresh()
    })
    return () => {
      live = false
      clearInterval(timer)
      unsubscribe()
    }
  }, [])

  return pending
}

// newlySeen returns the entries in current whose session_id was not
// present in seen — the pure diff behind App.tsx's "toast only on
// first-seen" rule, pulled out so it's testable without rendering the
// app: a raw poll/signal refetch of an unchanged list must not re-diff
// as new.
export function newlySeen(
  seen: ReadonlySet<string>,
  current: PendingPermission[],
): PendingPermission[] {
  return current.filter((p) => !seen.has(p.session_id))
}

// toastSessionLabel picks the approval toast's description: the
// session's title once generated, else a static fallback — a brand
// new session's title generates async (chat.TitleOverGateway) and can
// still be empty when the first permission_request fires, so the
// toast must never render the raw empty string as "Untitled session".
export function toastSessionLabel(p: PendingPermission): string {
  return p.session_title || 'Chat'
}
