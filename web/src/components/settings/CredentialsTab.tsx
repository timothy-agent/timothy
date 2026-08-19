import { Delete02Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useCallback, useEffect, useState } from 'react'
import { toast } from 'sonner'
import { deleteSecret, listSecretRefs, migrateAllSecrets, type SecretRefEntry } from '../../api/client'
import { Button } from '../ui/button'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '../ui/dialog'
import { useDefaultSecretBackend } from './useDefaultSecretBackend'
import { errText } from './util'

const BACKEND_LABEL: Record<string, string> = {
  db: 'Timothy storage',
  vault: 'Vault',
  asm: 'AWS Secrets Manager',
}

// CredentialsTab is a read-only directory of every stored secret ref:
// name, used-by chips, and timestamps when the row has them. There is
// no reveal/show-value affordance anywhere — this lists what exists
// and what references it, never a value. Delete is only offered for
// orphaned refs; a referenced ref's delete button is replaced by its
// used-by chips explaining why.
export function CredentialsTab() {
  const [refs, setRefs] = useState<SecretRefEntry[]>([])
  const [loaded, setLoaded] = useState(false)
  const [pendingDelete, setPendingDelete] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [migrating, setMigrating] = useState(false)
  const defaultBackend = useDefaultSecretBackend()

  const refresh = useCallback(() => {
    listSecretRefs()
      .then((rows) => {
        setRefs(rows)
        setLoaded(true)
      })
      .catch((err: unknown) => toast.error('Could not load credentials', { description: errText(err) }))
  }, [])
  useEffect(refresh, [refresh])

  const remove = async (name: string) => {
    setBusy(true)
    try {
      await deleteSecret(name)
      toast.success('Credential removed', { description: `${name} is gone from the store.` })
      setPendingDelete(null)
      refresh()
    } catch (err) {
      toast.error('Could not remove credential', { description: errText(err) })
    } finally {
      setBusy(false)
    }
  }

  // Migrate all: only offered once an external backend is the default
  // (moving everything back to "db" isn't this button's job) and at
  // least one ref still lives elsewhere.
  const elsewhereCount = refs.filter((r) => r.backend !== defaultBackend).length
  const showMigrateAll = defaultBackend !== 'db' && elsewhereCount > 0

  const migrateAll = async () => {
    setMigrating(true)
    try {
      const results = await migrateAllSecrets(defaultBackend)
      const migrated = results.filter((r) => r.migrated).length
      const failed = results.filter((r) => r.error)
      if (failed.length > 0) {
        toast.error(`Migrated ${migrated}, ${failed.length} failed`, {
          description: failed.map((f) => `${f.name}: ${f.error}`).join('; '),
        })
      } else {
        toast.success(`Migrated ${migrated} credential${migrated === 1 ? '' : 's'}`, {
          description: `Now stored in ${BACKEND_LABEL[defaultBackend] ?? defaultBackend}.`,
        })
      }
      refresh()
    } catch (err) {
      toast.error('Could not migrate credentials', { description: errText(err) })
    } finally {
      setMigrating(false)
    }
  }

  return (
    <div className="mt-6 space-y-4">
      <p className="text-sm text-muted-foreground">
        Every credential Timothy has stored, by reference name. Values are never shown here —
        this is a directory, not a vault viewer. A credential in use by a provider or connector
        can&apos;t be deleted until nothing references it.
      </p>
      {showMigrateAll && (
        <div className="flex items-center gap-3 rounded-xl border border-border p-4">
          <div className="min-w-0 flex-1 text-sm">
            {elsewhereCount} credential{elsewhereCount === 1 ? '' : 's'} not yet in{' '}
            {BACKEND_LABEL[defaultBackend] ?? defaultBackend}.
          </div>
          <Button size="sm" disabled={migrating} onClick={() => void migrateAll()}>
            Migrate all to {BACKEND_LABEL[defaultBackend] ?? defaultBackend}
          </Button>
        </div>
      )}
      {loaded && refs.length === 0 ? (
        <div className="rounded-xl border border-dashed border-border p-10 text-center text-sm text-muted-foreground">
          No credentials stored yet.
        </div>
      ) : (
        <div className="overflow-hidden rounded-xl border border-border">
          <table className="w-full text-sm">
            <thead className="bg-muted/40 text-xs uppercase tracking-wide text-muted-foreground">
              <tr>
                <th className="px-4 py-2 text-left font-medium">Reference</th>
                <th className="px-4 py-2 text-left font-medium">Used by</th>
                <th className="px-4 py-2 text-left font-medium">Created</th>
                <th className="px-4 py-2 text-left font-medium">Updated</th>
                <th className="px-4 py-2" />
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {refs.map((r) => (
                <tr key={r.name}>
                  <td className="px-4 py-2 font-mono text-xs">{r.name}</td>
                  <td className="px-4 py-2">
                    {r.referenced_by.length === 0 ? (
                      <span className="text-xs text-muted-foreground">orphaned</span>
                    ) : (
                      <div className="flex flex-wrap gap-1">
                        {r.referenced_by.map((ref) => (
                          <span
                            key={`${ref.kind}-${ref.name}`}
                            className="rounded bg-muted px-1.5 py-0.5 text-[10px] font-medium uppercase text-muted-foreground"
                          >
                            {ref.kind}: {ref.name}
                          </span>
                        ))}
                      </div>
                    )}
                  </td>
                  <td className="px-4 py-2 text-xs text-muted-foreground">
                    {r.created_at ? new Date(r.created_at).toLocaleDateString() : '—'}
                  </td>
                  <td className="px-4 py-2 text-xs text-muted-foreground">
                    {r.updated_at ? new Date(r.updated_at).toLocaleDateString() : '—'}
                  </td>
                  <td className="px-4 py-2 text-right">
                    {r.referenced_by.length === 0 && (
                      <button
                        type="button"
                        aria-label={`Delete ${r.name}`}
                        onClick={() => setPendingDelete(r.name)}
                        className="text-muted-foreground hover:text-red-500"
                      >
                        <HugeiconsIcon icon={Delete02Icon} className="size-4" />
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <Dialog open={pendingDelete != null} onOpenChange={(open) => !open && setPendingDelete(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete {pendingDelete}?</DialogTitle>
          </DialogHeader>
          <p className="text-sm text-muted-foreground">
            This cannot be undone. The stored value is removed permanently.
          </p>
          <DialogFooter>
            <Button variant="outline" disabled={busy} onClick={() => setPendingDelete(null)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              disabled={busy}
              onClick={() => pendingDelete && void remove(pendingDelete)}
            >
              Delete
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
