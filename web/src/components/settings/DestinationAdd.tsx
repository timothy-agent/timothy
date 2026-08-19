import { ArrowLeft01Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useEffect, useState } from 'react'
import { Link, Navigate, useNavigate, useParams } from 'react-router'
import { toast } from 'sonner'
import { createDestination, listConnectors, patchDestination, testDestination } from '../../api/client'
import type { AdminConnector } from '../../api/types'
import { Button } from '../ui/button'
import { Input } from '../ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../ui/select'
import { Field } from './shared'
import { errText } from './util'

function slugify(v: string): string {
  return v
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
}

// DestinationAdd is kind-aware (email vs webhook) and its own page,
// mirroring ConnectorAdd's shape: the destination row is created
// (disabled) as part of testing, and a passing test enables it.
export function DestinationAdd() {
  const { kind } = useParams<{ kind: 'email' | 'webhook' }>()
  const navigate = useNavigate()

  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)
  const [test, setTest] = useState<{ ok: boolean; error?: string } | null>(null)
  const [createdID, setCreatedID] = useState<string | null>(null)

  // email fields
  const [connectors, setConnectors] = useState<AdminConnector[] | null>(null)
  const [connectorID, setConnectorID] = useState('')
  const [to, setTo] = useState('')

  // webhook fields
  const [url, setURL] = useState('')
  const [format, setFormat] = useState<'json' | 'text'>('json')

  useEffect(() => {
    if (kind !== 'email') return
    listConnectors()
      .then((rows) => setConnectors(rows.filter((c) => c.kind === 'google' && c.enabled)))
      .catch((err: unknown) => toast.error('Could not load connectors', { description: errText(err) }))
  }, [kind])

  if (kind !== 'email' && kind !== 'webhook') return <Navigate to="/settings/destinations" replace />

  const invalidate = () => {
    setTest(null)
    setCreatedID(null)
  }

  const config = kind === 'email' ? { connector_id: connectorID, to: to.trim() } : { url: url.trim(), format }

  const canTest =
    slugify(name) !== '' &&
    (kind === 'email' ? connectorID !== '' && to.trim() !== '' : url.trim() !== '')

  const runTest = async () => {
    setBusy(true)
    setTest(null)
    try {
      const id = await createDestination({
        name: slugify(name),
        kind,
        config,
        enabled: false,
      })
      setCreatedID(id)
      setTest(await testDestination(id))
    } catch (err) {
      setTest({ ok: false, error: errText(err) })
    } finally {
      setBusy(false)
    }
  }

  const submit = async () => {
    if (!test?.ok || !createdID) return
    // The destination was created disabled to test it; enabling it is
    // a separate concern from the create form itself, so route through
    // the same list-page toggle the manage page uses.
    setBusy(true)
    try {
      await patchDestination(createdID, { enabled: true })
      toast.success('Destination added')
      navigate('/settings/destinations')
    } catch (err) {
      toast.error('Could not enable destination', { description: errText(err) })
    } finally {
      setBusy(false)
    }
  }

  const tested = test?.ok === true

  return (
    <div className="mt-6 w-full space-y-6">
      <Link
        to="/settings/destinations"
        className="inline-flex w-fit items-center gap-1.5 text-sm text-muted-foreground transition hover:text-foreground"
      >
        <HugeiconsIcon icon={ArrowLeft01Icon} className="size-4" />
        Destinations
      </Link>

      <div className="border-b border-border pb-6">
        <h1 className="text-xl font-semibold tracking-tight">
          Add {kind === 'email' ? 'Email' : 'Webhook'} destination
        </h1>
        <p className="text-sm text-muted-foreground">kind: {kind}</p>
      </div>

      <div className="grid max-w-3xl gap-5">
        <Field label="Name" hint="lowercase slug">
          <Input
            value={name}
            onChange={(e) => {
              setName(e.target.value)
              invalidate()
            }}
            placeholder="ops-inbox"
            className="mt-1.5 h-10"
          />
        </Field>

        {kind === 'email' ? (
          <>
            <div className="space-y-1.5">
              <span className="text-sm font-medium text-foreground">Google connector</span>
              <Select
                value={connectorID}
                onValueChange={(v) => {
                  setConnectorID(v)
                  invalidate()
                }}
              >
                <SelectTrigger className="w-full">
                  <SelectValue placeholder="Choose a connected Gmail account" />
                </SelectTrigger>
                <SelectContent>
                  {(connectors ?? []).map((c) => (
                    <SelectItem key={c.id} value={c.id}>
                      {c.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              {connectors && connectors.length === 0 && (
                <p className="text-sm text-muted-foreground">
                  No enabled Google connectors yet — add one under Connectors first.
                </p>
              )}
            </div>
            <Field label="To" hint="recipient address">
              <Input
                value={to}
                onChange={(e) => {
                  setTo(e.target.value)
                  invalidate()
                }}
                placeholder="ops@example.com"
                className="mt-1.5 h-10"
              />
            </Field>
          </>
        ) : (
          <>
            <Field label="URL">
              <Input
                value={url}
                onChange={(e) => {
                  setURL(e.target.value)
                  invalidate()
                }}
                placeholder="https://…/hook"
                className="mt-1.5 h-10"
              />
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

        <div
          className={
            'flex flex-wrap items-center gap-3 rounded-xl border p-4 text-sm ' +
            (tested
              ? 'border-good/30 bg-good-soft text-good'
              : test && !test.ok
                ? 'border-destructive/30 bg-destructive/5 text-destructive'
                : 'border-border bg-muted/40 text-muted-foreground')
          }
        >
          <span className="min-w-0 flex-1 font-medium">
            {busy
              ? 'Testing…'
              : tested
                ? 'Test delivery sent, ready to add.'
                : test && !test.ok
                  ? `Test failed: ${test.error}. The destination was saved disabled, fix and retry.`
                  : 'Not tested yet, run a test before adding.'}
          </span>
          <Button size="sm" variant="test" disabled={busy || !canTest} onClick={() => void runTest()}>
            {busy ? 'Testing…' : 'Test send'}
          </Button>
        </div>

        <div className="flex gap-3 pt-2">
          <Button variant="outline" disabled={busy} onClick={() => navigate('/settings/destinations')}>
            Cancel
          </Button>
          <Button disabled={!tested || busy} onClick={() => void submit()}>
            Add destination
          </Button>
        </div>
      </div>
    </div>
  )
}
