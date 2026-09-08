import { Trash2 } from 'lucide-react'
import { useCallback, useEffect, useState } from 'react'
import { toast } from 'sonner'
import { deleteSecret, listSecretRefs, migrateAllSecrets, type SecretRefEntry } from '../../api/client'
import { Button } from '../ui/button'
import { Badge } from '../ui/badge'
import { Alert, AlertDescription } from '../ui/alert'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '../ui/table'
import { ConfirmDialog } from '../timothy/confirm-dialog'
import { IconButton } from '../timothy/icon-button'
import { Panel } from '../timothy/panel'
import { PageHeader } from '../timothy/page-header'
import { PageShell } from '../timothy/page-shell'
import { settingsArea } from './settingsAreas'
import { useDefaultSecretBackend } from './useDefaultSecretBackend'
import { errText } from './util'

const area = settingsArea('credentials')

const BACKEND_LABEL: Record<string, string> = {
  db: 'Timothy storage',
  vault: 'Vault',
  asm: 'AWS Secrets Manager',
}

// CredentialsTab is a read-only directory of every stored secret ref:
// name, used-by chips, and timestamps when the row has them. There is
// no reveal/show-value affordance anywhere. This lists what exists
// and what references it, never a value. Delete is only offered for
// orphaned refs; a referenced ref's delete button is replaced by its
// used-by chips explaining why. A "system" ref (a configured secret
// backend's own bootstrap credential, e.g. the vault token) gets a
// System badge instead and its delete action is disabled with an
// explanatory label. The gateway refuses the delete regardless
// (secretstore.Delete's own guard), this is purely cosmetic.
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
  // least one ref still lives elsewhere. System refs (a backend's own
  // bootstrap credential) don't count: they can never migrate, so
  // counting them left the banner nagging forever.
  const elsewhereCount = refs.filter((r) => r.backend !== defaultBackend && !r.system).length
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
    <PageShell>
      <PageHeader
        title={area.label}
        description={area.description}
        breadcrumbs={[{ label: 'Settings', href: '/settings' }, { label: area.label }]}
      />
      <div className="space-y-6">
        <p className="text-sm text-muted-foreground">
          Every credential Timothy has stored, by reference name. Values are never shown here.
          This is a directory, not a vault viewer. A credential in use by a provider or connector
          can&apos;t be deleted until nothing references it.
        </p>
        {showMigrateAll && (
          <Alert tone="info">
            <AlertDescription className="flex items-center gap-3">
              <div className="min-w-0 flex-1">
                {elsewhereCount} credential{elsewhereCount === 1 ? '' : 's'} not yet in{' '}
                {BACKEND_LABEL[defaultBackend] ?? defaultBackend}.
              </div>
              <Button size="sm" disabled={migrating} onClick={() => void migrateAll()}>
                Migrate all to {BACKEND_LABEL[defaultBackend] ?? defaultBackend}
              </Button>
            </AlertDescription>
          </Alert>
        )}
        <Panel title="Credentials" density="operational">
          {loaded && refs.length === 0 ? (
            <p className="p-4 text-sm text-muted-foreground">No credentials stored yet.</p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Reference</TableHead>
                  <TableHead>Used by</TableHead>
                  <TableHead>Created</TableHead>
                  <TableHead>Updated</TableHead>
                  <TableHead>
                    <span className="sr-only">Actions</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {refs.map((r) => (
                  <TableRow key={r.name}>
                    <TableCell className="font-mono text-xs">{r.name}</TableCell>
                    <TableCell>
                      {r.system ? (
                        <Badge variant="warning" size="sm" className="uppercase tracking-wide">
                          System
                        </Badge>
                      ) : r.referenced_by.length === 0 ? (
                        <span className="text-xs text-muted-foreground">orphaned</span>
                      ) : (
                        <div className="flex flex-wrap gap-1">
                          {r.referenced_by.map((ref) => (
                            <Badge key={`${ref.kind}-${ref.name}`} variant="neutral" size="sm" className="uppercase">
                              {ref.kind}: {ref.name}
                            </Badge>
                          ))}
                        </div>
                      )}
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {r.created_at ? new Date(r.created_at).toLocaleDateString() : '-'}
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {r.updated_at ? new Date(r.updated_at).toLocaleDateString() : '-'}
                    </TableCell>
                    <TableCell className="text-right">
                      {r.system ? (
                        <IconButton
                          label={`${r.name} is the bootstrap credential for the ${BACKEND_LABEL[r.backend] ?? r.backend} secret backend and can't be deleted while it's configured.`}
                          icon={Trash2}
                          size="xs"
                          disabled
                        />
                      ) : (
                        r.referenced_by.length === 0 && (
                          <IconButton
                            label={`Delete ${r.name}`}
                            icon={Trash2}
                            size="xs"
                            onClick={() => setPendingDelete(r.name)}
                          />
                        )
                      )}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </Panel>

        <ConfirmDialog
          open={pendingDelete != null}
          onOpenChange={(open) => !open && setPendingDelete(null)}
          title={`Delete ${pendingDelete}?`}
          description="This cannot be undone. The stored value is removed permanently."
          confirmLabel="Delete"
          destructive
          loading={busy}
          onConfirm={() => pendingDelete && void remove(pendingDelete)}
        />
      </div>
    </PageShell>
  )
}
