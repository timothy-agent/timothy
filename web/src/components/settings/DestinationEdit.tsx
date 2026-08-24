import { ArrowLeft01Icon, Delete02Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useCallback, useEffect, useState } from 'react'
import { Link, Navigate, useNavigate, useParams } from 'react-router'
import { toast } from 'sonner'
import {
  deleteDestination,
  listConnectors,
  listDestinations,
  patchDestination,
  setSecret,
  testDestination,
} from '../../api/client'
import type { AdminConnector, Destination } from '../../api/types'
import { Button } from '../ui/button'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '../ui/dialog'
import { Input } from '../ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../ui/select'
import { CredentialModeToggle, ExistingCredentialSelect, type CredentialMode } from './CredentialRefPicker'
import { Field, Toggle } from './shared'
import { errText } from './util'

export function DestinationEdit() {
  const { id } = useParams()
  const navigate = useNavigate()

  const [destination, setDestination] = useState<Destination | null | undefined>(undefined)
  const [connectors, setConnectors] = useState<AdminConnector[]>([])
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [test, setTest] = useState<{ ok: boolean; error?: string } | null>(null)
  const [testing, setTesting] = useState(false)

  // Editable config fields, seeded from the loaded destination.
  const [to, setTo] = useState('')
  const [connectorID, setConnectorID] = useState('')
  const [url, setURL] = useState('')
  const [format, setFormat] = useState<'json' | 'text'>('json')
  const [chatID, setChatID] = useState('')
  const [savingConfig, setSavingConfig] = useState(false)

  // Rotating the bot token is a separate save from the rest of the
  // config: it writes credential_ref's value, not config.
  const [botToken, setBotToken] = useState('')
  const [botTokenMode, setBotTokenMode] = useState<CredentialMode>('new')
  const [existingBotTokenRef, setExistingBotTokenRef] = useState('')
  const [savingToken, setSavingToken] = useState(false)

  const refresh = useCallback(() => {
    listDestinations()
      .then((list) => {
        const found = list.find((d) => d.id === id) ?? null
        setDestination(found)
        if (found?.kind === 'email') {
          setConnectorID(String(found.config.connector_id ?? ''))
          setTo(String(found.config.to ?? ''))
        } else if (found?.kind === 'webhook') {
          setURL(String(found.config.url ?? ''))
          setFormat((found.config.format as 'json' | 'text') ?? 'json')
        } else if (found?.kind === 'telegram') {
          setChatID(String(found.config.chat_id ?? ''))
        }
      })
      .catch((err: unknown) => toast.error('Could not load destination', { description: errText(err) }))
  }, [id])
  useEffect(refresh, [refresh])

  useEffect(() => {
    listConnectors()
      .then((rows) => setConnectors(rows.filter((c) => c.kind === 'google' && c.enabled)))
      .catch(() => {
        // Non-fatal: the connector select just shows the currently
        // stored id with no friendly name if this fails.
      })
  }, [])

  const remove = async () => {
    if (!destination) return
    try {
      await deleteDestination(destination.id)
      toast.success('Destination removed')
      navigate('/settings/destinations')
    } catch (err) {
      toast.error('Could not remove destination', { description: errText(err) })
      setConfirmDelete(false)
    }
  }

  const runTest = async () => {
    if (!destination) return
    setTesting(true)
    setTest(null)
    try {
      setTest(await testDestination(destination.id))
    } catch (err) {
      setTest({ ok: false, error: errText(err) })
    } finally {
      setTesting(false)
    }
  }

  const toggleEnabled = (enabled: boolean) => {
    if (!destination) return
    patchDestination(destination.id, { enabled })
      .then(refresh)
      .catch((err: unknown) => toast.error('Could not update destination', { description: errText(err) }))
  }

  const saveConfig = async () => {
    if (!destination) return
    setSavingConfig(true)
    try {
      const config =
        destination.kind === 'email'
          ? { connector_id: connectorID, to: to.trim() }
          : destination.kind === 'telegram'
            ? { chat_id: chatID.trim() }
            : { url: url.trim(), format }
      await patchDestination(destination.id, { config })
      toast.success('Destination updated')
      refresh()
    } catch (err) {
      toast.error('Could not update destination', { description: errText(err) })
    } finally {
      setSavingConfig(false)
    }
  }

  const usingExistingBotToken = botTokenMode === 'existing'

  const saveBotToken = async () => {
    if (!destination) return
    setSavingToken(true)
    try {
      const ref = usingExistingBotToken ? existingBotTokenRef : destination.credential_ref
      if (!usingExistingBotToken) await setSecret(ref, botToken.trim())
      await patchDestination(destination.id, { credential_ref: ref })
      toast.success('Bot token updated')
      setBotToken('')
      refresh()
    } catch (err) {
      toast.error('Could not update bot token', { description: errText(err) })
    } finally {
      setSavingToken(false)
    }
  }

  if (destination === null) return <Navigate to="/settings/destinations" replace />
  if (destination === undefined) return null

  return (
    <div className="mt-6 w-full space-y-6">
      <Link
        to="/settings/destinations"
        className="inline-flex w-fit items-center gap-1.5 text-sm text-muted-foreground transition hover:text-foreground"
      >
        <HugeiconsIcon icon={ArrowLeft01Icon} className="size-4" />
        Destinations
      </Link>

      <div className="flex items-center gap-4 border-b border-border pb-6">
        <div className="min-w-0 flex-1">
          <h1 className="truncate text-xl font-semibold tracking-tight">{destination.name}</h1>
          <p className="text-sm text-muted-foreground uppercase">{destination.kind}</p>
        </div>
        <Button variant="destructive" onClick={() => setConfirmDelete(true)}>
          <HugeiconsIcon icon={Delete02Icon} />
          Delete
        </Button>
      </div>

      <div className="grid max-w-3xl gap-5">
        <div className="flex items-center justify-between gap-4 rounded-xl border border-border p-4">
          <div className="min-w-0">
            <div className="text-sm font-medium">Enabled</div>
            <p className="text-sm text-muted-foreground">
              Disabled destinations are skipped by mission delivery without an error.
            </p>
          </div>
          <Toggle on={destination.enabled} onChange={toggleEnabled} label={`${destination.name} enabled`} />
        </div>

        <div
          className={
            'flex flex-wrap items-center gap-3 rounded-xl border p-4 text-sm ' +
            (test?.ok
              ? 'border-good/30 bg-good-soft text-good'
              : test && !test.ok
                ? 'border-destructive/30 bg-destructive/5 text-destructive'
                : 'border-border bg-muted/40 text-muted-foreground')
          }
        >
          <span className="min-w-0 flex-1 font-medium">
            {testing
              ? 'Sending test delivery…'
              : test?.ok
                ? 'Test delivery sent.'
                : test && !test.ok
                  ? `Failed: ${test.error}`
                  : 'Not tested yet.'}
          </span>
          <Button size="sm" variant="test" disabled={testing} onClick={() => void runTest()}>
            {testing ? 'Sending…' : 'Test send'}
          </Button>
        </div>

        {destination.kind === 'email' ? (
          <>
            <div className="space-y-1.5">
              <span className="text-sm font-medium text-foreground">Google connector</span>
              <Select value={connectorID} onValueChange={setConnectorID}>
                <SelectTrigger className="w-full">
                  <SelectValue placeholder="Choose a connected Gmail account" />
                </SelectTrigger>
                <SelectContent>
                  {connectors.map((c) => (
                    <SelectItem key={c.id} value={c.id}>
                      {c.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <Field label="To">
              <Input value={to} onChange={(e) => setTo(e.target.value)} className="mt-1.5 h-10" />
            </Field>
          </>
        ) : destination.kind === 'telegram' ? (
          <>
            <Field label="Chat ID">
              <Input value={chatID} onChange={(e) => setChatID(e.target.value)} className="mt-1.5 h-10" />
            </Field>
          </>
        ) : (
          <>
            <Field label="URL">
              <Input value={url} onChange={(e) => setURL(e.target.value)} className="mt-1.5 h-10" />
            </Field>
            <div className="space-y-1.5">
              <span className="text-sm font-medium text-foreground">Format</span>
              <Select value={format} onValueChange={(v) => setFormat(v as 'json' | 'text')}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="json">JSON</SelectItem>
                  <SelectItem value="text">Plain text</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </>
        )}

        <div className="flex gap-3 pt-2">
          <Button disabled={savingConfig} onClick={() => void saveConfig()}>
            {savingConfig ? 'Saving…' : 'Save changes'}
          </Button>
        </div>

        {destination.kind === 'telegram' && (
          <div className="space-y-3 rounded-xl border border-border p-4">
            <div className="flex items-center justify-between">
              <span className="text-sm font-medium text-foreground">Rotate bot token</span>
              <CredentialModeToggle
                mode={botTokenMode}
                onChange={(m) => {
                  setBotTokenMode(m)
                  if (m === 'existing' && !existingBotTokenRef) setExistingBotTokenRef(destination.credential_ref)
                }}
                labels={{ new: 'New token', existing: 'Different credential' }}
              />
            </div>
            {botTokenMode === 'existing' ? (
              <Field label="Existing credential">
                <ExistingCredentialSelect value={existingBotTokenRef} onChange={setExistingBotTokenRef} />
              </Field>
            ) : (
              <div className="space-y-1.5">
                <Input
                  value={botToken}
                  onChange={(e) => setBotToken(e.target.value)}
                  placeholder="123456:ABC-DEF..."
                  className="h-10"
                />
                <p className="text-xs text-muted-foreground">
                  Replaces the stored value of {destination.credential_ref}.
                </p>
              </div>
            )}
            <Button
              size="sm"
              disabled={savingToken || (usingExistingBotToken ? !existingBotTokenRef : !botToken.trim())}
              onClick={() => void saveBotToken()}
            >
              {savingToken ? 'Saving…' : 'Save token'}
            </Button>
          </div>
        )}
      </div>

      <Dialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete {destination.name}?</DialogTitle>
          </DialogHeader>
          <p className="text-sm text-muted-foreground">
            Removes the destination. Refused while any in-progress mission still delivers to it.
          </p>
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfirmDelete(false)}>
              Cancel
            </Button>
            <Button variant="destructive" onClick={() => void remove()}>
              Delete
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
