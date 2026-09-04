import { ArrowLeft01Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useEffect, useState } from 'react'
import { Link, Navigate, useNavigate, useParams } from 'react-router'
import { toast } from 'sonner'
import { createDestination, listConnectors, patchDestination, setSecret, testDestination } from '../../api/client'
import type { AdminConnector } from '../../api/types'
import {
  COMMIT_STYLE_DEFAULT,
  ON_COMPLETE_NONE,
  commitStyleChoices,
  onCompleteChoices,
} from '../../lib/githubDestination'
import { Button } from '../ui/button'
import { Input } from '../ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../ui/select'
import { CredentialModeToggle, ExistingCredentialSelect, type CredentialMode } from './CredentialRefPicker'
import { Field, Toggle } from './shared'
import { errText } from './util'

function slugify(v: string): string {
  return v
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
}

// DestinationAdd is kind-aware (email vs webhook vs telegram) and its
// own page, mirroring ConnectorAdd's shape: the destination row is
// created (disabled) as part of testing, and a passing test enables
// it.
export function DestinationAdd() {
  const { kind } = useParams<{ kind: 'email' | 'webhook' | 'telegram' | 'github' }>()
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

  // telegram fields
  const [chatID, setChatID] = useState('')
  const [botToken, setBotToken] = useState('')
  const [botTokenMode, setBotTokenMode] = useState<CredentialMode>('new')
  const [existingBotTokenRef, setExistingBotTokenRef] = useState('')

  // github fields (reuses connectorID above for the picked connector)
  const [mode, setMode] = useState<'push' | 'push_pr'>('push')
  const [branchPattern, setBranchPattern] = useState('')
  const [commitStyle, setCommitStyle] = useState('')
  const [createIfMissing, setCreateIfMissing] = useState(false)

  useEffect(() => {
    if (kind !== 'email' && kind !== 'github') return
    const wantKind = kind === 'email' ? 'google' : 'github'
    listConnectors()
      .then((rows) => setConnectors(rows.filter((c) => c.kind === wantKind && c.enabled)))
      .catch((err: unknown) => toast.error('Could not load connectors', { description: errText(err) }))
  }, [kind])

  if (kind !== 'email' && kind !== 'webhook' && kind !== 'telegram' && kind !== 'github') {
    return <Navigate to="/settings/destinations" replace />
  }

  const invalidate = () => {
    setTest(null)
    setCreatedID(null)
  }

  const slug = slugify(name)
  const usingExistingBotToken = botTokenMode === 'existing'
  const botTokenRef = usingExistingBotToken ? existingBotTokenRef : `${slug.toUpperCase().replace(/-/g, '_')}_TELEGRAM_BOT_TOKEN`

  const config =
    kind === 'email'
      ? { connector_id: connectorID, to: to.trim() }
      : kind === 'telegram'
        ? { chat_id: chatID.trim() }
        : kind === 'github'
          ? {
              connector_id: connectorID,
              mode,
              branch_pattern: branchPattern.trim() || undefined,
              commit_style: commitStyle || undefined,
              create_if_missing: createIfMissing || undefined,
            }
          : { url: url.trim(), format }

  const canTest =
    slug !== '' &&
    (kind === 'email'
      ? connectorID !== '' && to.trim() !== ''
      : kind === 'telegram'
        ? chatID.trim() !== '' && (usingExistingBotToken ? existingBotTokenRef !== '' : botToken.trim() !== '')
        : kind === 'github'
          ? connectorID !== ''
          : url.trim() !== '')

  const runTest = async () => {
    setBusy(true)
    setTest(null)
    try {
      if (kind === 'telegram' && !usingExistingBotToken) await setSecret(botTokenRef, botToken.trim())
      const id = await createDestination({
        name: slug,
        kind,
        config,
        credential_ref: kind === 'telegram' ? botTokenRef : undefined,
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

  // github destinations cannot be test-sent (no test-send affordance
  // for this kind), so adding one creates it enabled directly.
  const submitGitHub = async () => {
    if (!canTest) return
    setBusy(true)
    try {
      await createDestination({ name: slug, kind: 'github', config, enabled: true })
      toast.success('Destination added')
      navigate('/settings/destinations')
    } catch (err) {
      toast.error('Could not add destination', { description: errText(err) })
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
          Add {kind === 'email' ? 'Email' : kind === 'telegram' ? 'Telegram' : kind === 'github' ? 'GitHub' : 'Webhook'}{' '}
          destination
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
        ) : kind === 'telegram' ? (
          <>
            <Field label="Chat ID" hint="the numeric chat or channel id the bot posts to">
              <Input
                value={chatID}
                onChange={(e) => {
                  setChatID(e.target.value)
                  invalidate()
                }}
                placeholder="123456789"
                className="mt-1.5 h-10"
              />
            </Field>
            <div className="space-y-3">
              <div className="flex items-center justify-between">
                <span className="text-sm font-medium text-foreground">Bot token</span>
                <CredentialModeToggle
                  mode={botTokenMode}
                  onChange={(m) => {
                    setBotTokenMode(m)
                    invalidate()
                  }}
                />
              </div>
              {botTokenMode === 'existing' ? (
                <Field label="Existing credential">
                  <ExistingCredentialSelect
                    value={existingBotTokenRef}
                    onChange={(v) => {
                      setExistingBotTokenRef(v)
                      invalidate()
                    }}
                  />
                </Field>
              ) : (
                <Input
                  value={botToken}
                  onChange={(e) => {
                    setBotToken(e.target.value)
                    invalidate()
                  }}
                  placeholder="123456:ABC-DEF..."
                  className="h-10"
                />
              )}
            </div>
          </>
        ) : kind === 'github' ? (
          <>
            <div className="space-y-1.5">
              <span className="text-sm font-medium text-foreground">GitHub connector</span>
              <Select
                value={connectorID}
                onValueChange={(v) => {
                  setConnectorID(v)
                  invalidate()
                }}
              >
                <SelectTrigger className="w-full">
                  <SelectValue placeholder="Choose a connected GitHub account" />
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
                  No enabled GitHub connectors yet, add one under Connectors first.
                </p>
              )}
            </div>
            <div className="space-y-1.5">
              <span className="text-sm font-medium text-foreground">Mode</span>
              <Select
                value={mode}
                onValueChange={(v) => {
                  setMode(v as 'push' | 'push_pr')
                  invalidate()
                }}
              >
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {onCompleteChoices
                    .filter((c) => c.value !== ON_COMPLETE_NONE)
                    .map((c) => (
                      <SelectItem key={c.value} value={c.value}>
                        {c.label}
                      </SelectItem>
                    ))}
                </SelectContent>
              </Select>
            </div>
            <Field label="Branch pattern" hint="optional">
              <Input
                value={branchPattern}
                onChange={(e) => {
                  setBranchPattern(e.target.value)
                  invalidate()
                }}
                placeholder="Default (from settings)"
                className="mt-1.5 h-10"
              />
            </Field>
            <div className="space-y-1.5">
              <span className="text-sm font-medium text-foreground">Commit style</span>
              <Select
                value={commitStyle || COMMIT_STYLE_DEFAULT}
                onValueChange={(v) => {
                  setCommitStyle(v === COMMIT_STYLE_DEFAULT ? '' : v)
                  invalidate()
                }}
              >
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {commitStyleChoices.map((c) => (
                    <SelectItem key={c.value} value={c.value}>
                      {c.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="flex items-center justify-between gap-4 rounded-xl border border-border p-4">
              <div className="min-w-0">
                <div className="text-sm font-medium">Create repository if missing</div>
                <p className="text-sm text-muted-foreground">
                  Create the repository through this connector when the mission has no target
                  repository.
                </p>
              </div>
              <Toggle
                on={createIfMissing}
                onChange={(v) => {
                  setCreateIfMissing(v)
                  invalidate()
                }}
                label="Create repository if missing"
              />
            </div>
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

        {kind !== 'github' && (
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
        )}

        <div className="flex gap-3 pt-2">
          <Button variant="outline" disabled={busy} onClick={() => navigate('/settings/destinations')}>
            Cancel
          </Button>
          {kind === 'github' ? (
            <Button disabled={!canTest || busy} onClick={() => void submitGitHub()}>
              {busy ? 'Adding…' : 'Add destination'}
            </Button>
          ) : (
            <Button disabled={!tested || busy} onClick={() => void submit()}>
              Add destination
            </Button>
          )}
        </div>
      </div>
    </div>
  )
}
