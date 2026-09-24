import { Trash2 } from 'lucide-react'
import { useCallback, useEffect, useState } from 'react'
import { Navigate, useNavigate, useParams } from 'react-router'
import { toast } from 'sonner'
import { deleteChannel, getChannel, listAgents, patchChannel, setSecret, testChannel } from '../../api/client'
import type { AdminAgent, Channel } from '../../api/types'
import { Button } from '../ui/button'
import { Input } from '../ui/input'
import { Switch } from '../ui/switch'
import { Alert, AlertDescription } from '../ui/alert'
import { ConfirmDialog } from '../timothy/confirm-dialog'
import { Field, FieldGroup, Form, FormActions } from '../timothy/field'
import { Panel } from '../timothy/panel'
import { PageHeader } from '../timothy/page-header'
import { PageShell } from '../timothy/page-shell'
import { ChannelAgentSelect } from './ChannelAgentSelect'
import { channelAgentPatch, channelAgentValue } from './channelAgent'
import { ChannelPairings } from './ChannelPairings'
import { CredentialField, type CredentialMode } from './CredentialRefPicker'
import { settingsArea } from './settingsAreas'
import { TestStatus } from './TestStatus'
import { useDefaultSecretBackend } from './useDefaultSecretBackend'
import { useStagedForm } from './useStagedForm'
import { errText } from '../../lib/errors'

const area = settingsArea('channels')

interface ChannelValues {
  name: string
  agent: string
}

function valuesFrom(channel: Channel): ChannelValues {
  return { name: channel.name, agent: channelAgentValue(channel) }
}

// ChannelEdit loads the channel, then mounts the form keyed by id so the
// staged baseline starts from real data.
export function ChannelEdit() {
  const { id = '' } = useParams()
  const [channel, setChannel] = useState<Channel | null | undefined>(undefined)

  const refresh = useCallback(
    () =>
      getChannel(id)
        .then((c) => {
          setChannel(c)
          return c
        })
        .catch((err: unknown) => {
          if ((err as { status?: number }).status === 404) setChannel(null)
          else toast.error('Could not load channel', { description: errText(err) })
          return undefined
        }),
    [id],
  )
  useEffect(() => {
    void refresh()
  }, [refresh])

  if (channel === null) return <Navigate to="/settings/channels" replace />
  if (channel === undefined) return null
  return <ChannelEditForm key={channel.id} initial={channel} refresh={refresh} />
}

function ChannelEditForm({ initial, refresh }: { initial: Channel; refresh: () => Promise<Channel | undefined> }) {
  const navigate = useNavigate()
  const defaultBackend = useDefaultSecretBackend()
  const [channel, setChannel] = useState(initial)
  const [agents, setAgents] = useState<AdminAgent[]>([])
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [testing, setTesting] = useState(false)
  const [test, setTest] = useState<{ ok: boolean; message: string } | null>(null)
  const [token, setToken] = useState('')
  const [tokenMode, setTokenMode] = useState<CredentialMode>('new')
  const [existingRef, setExistingRef] = useState('')
  const [savingToken, setSavingToken] = useState(false)
  const [appToken, setAppToken] = useState('')
  const [appTokenMode, setAppTokenMode] = useState<CredentialMode>('new')
  const [existingAppRef, setExistingAppRef] = useState('')
  const [savingAppToken, setSavingAppToken] = useState(false)
  const slack = channel.kind === 'slack'
  const service = slack ? 'Slack' : 'Telegram'
  const staged = useStagedForm<ChannelValues>(valuesFrom(initial))

  useEffect(() => {
    listAgents()
      .then(setAgents)
      .catch(() => {
        // Non-fatal: the select still offers default and dispatch.
      })
  }, [])

  const doRefresh = useCallback(async () => {
    const c = await refresh()
    if (c) setChannel(c)
    return c
  }, [refresh])

  const save = async () => {
    setSaving(true)
    setSaveError(null)
    try {
      const updated = await patchChannel(channel.id, { name: staged.values.name.trim(), ...channelAgentPatch(staged.values.agent) })
      setChannel(updated)
      staged.rebase(valuesFrom(updated))
      toast.success('Channel updated')
    } catch (err) {
      setSaveError(errText(err))
    } finally {
      setSaving(false)
    }
  }

  const toggleEnabled = (enabled: boolean) => {
    patchChannel(channel.id, { enabled }).then(setChannel, (err: unknown) =>
      toast.error('Could not update channel', { description: errText(err) }),
    )
  }

  const runTest = async () => {
    setTesting(true)
    setTest(null)
    try {
      const res = await testChannel(channel.id)
      setTest({ ok: true, message: `Connected as @${res.bot_username}.` })
      void doRefresh()
    } catch (err) {
      setTest({ ok: false, message: `Failed: ${errText(err)}` })
    } finally {
      setTesting(false)
    }
  }

  const usingExisting = tokenMode === 'existing'
  const saveToken = async () => {
    setSavingToken(true)
    try {
      const ref = usingExisting ? existingRef : channel.credential_ref
      if (!usingExisting) await setSecret(ref, token.trim())
      setChannel(await patchChannel(channel.id, { credential_ref: ref }))
      setToken('')
      toast.success('Bot token updated')
    } catch (err) {
      toast.error('Could not update bot token', { description: errText(err) })
    } finally {
      setSavingToken(false)
    }
  }

  const usingExistingApp = appTokenMode === 'existing'
  const saveAppToken = async () => {
    setSavingAppToken(true)
    try {
      const ref = usingExistingApp ? existingAppRef : (channel.config.app_token_ref ?? '')
      if (!usingExistingApp) await setSecret(ref, appToken.trim())
      setChannel(await patchChannel(channel.id, { config: { app_token_ref: ref } }))
      setAppToken('')
      toast.success('App token updated')
    } catch (err) {
      toast.error('Could not update app token', { description: errText(err) })
    } finally {
      setSavingAppToken(false)
    }
  }

  const remove = async () => {
    try {
      await deleteChannel(channel.id)
      toast.success('Channel removed')
      navigate('/settings/channels')
    } catch (err) {
      toast.error('Could not remove channel', { description: errText(err) })
      setConfirmDelete(false)
    }
  }

  return (
    <PageShell width="form">
      <PageHeader
        title={channel.name}
        description={channel.config.bot_username ? `${channel.kind} · @${channel.config.bot_username}` : channel.kind}
        breadcrumbs={[
          { label: 'Settings', href: '/settings' },
          { label: area.label, href: '/settings/channels' },
          { label: channel.name },
        ]}
        actions={
          <Button variant="destructive" onClick={() => setConfirmDelete(true)}>
            <Trash2 />
            Delete
          </Button>
        }
      />

      <div className="mb-8 flex items-center justify-between gap-4 rounded-md border border-border p-4">
        <div className="min-w-0">
          <div className="text-sm font-medium">Enabled</div>
          <p className="text-sm text-muted-foreground">
            {slack
              ? 'Disabled channels close their Slack connection; messages sent meanwhile are not answered.'
              : 'Disabled channels stop polling; messages sent meanwhile wait at Telegram.'}
          </p>
        </div>
        <Switch checked={channel.enabled} onCheckedChange={toggleEnabled} aria-label={`${channel.name} enabled`} />
      </div>

      <Form
        onSubmit={(e) => {
          e.preventDefault()
          void save()
        }}
      >
        <FieldGroup>
          <Field label="Name" description="unique">
            <Input value={staged.values.name} onChange={(e) => staged.setField('name', e.target.value)} />
          </Field>
          <Field label="Agent" description="who answers new conversations">
            <ChannelAgentSelect value={staged.values.agent} onChange={(v) => staged.setField('agent', v)} agents={agents} />
          </Field>
        </FieldGroup>
        {saveError && (
          <Alert tone="destructive">
            <AlertDescription>Could not save channel changes: {saveError}</AlertDescription>
          </Alert>
        )}
        <FormActions note={staged.dirty ? 'Unsaved changes' : undefined}>
          <Button type="button" variant="outline" disabled={saving} onClick={staged.reset}>
            Cancel
          </Button>
          <Button type="submit" disabled={!staged.dirty || saving || staged.values.name.trim() === ''}>
            Save
          </Button>
        </FormActions>
      </Form>

      <div className="mt-10 space-y-4">
        <ChannelPairings channelID={channel.id} />

        <Panel title="Test connection">
          {test || testing ? (
            <TestStatus
              state={testing ? 'testing' : test?.ok ? 'ok' : 'failed'}
              message={testing ? 'Testing…' : test?.message}
              action={
                !testing && (
                  <Button size="sm" variant="test" onClick={() => void runTest()}>
                    Test connection
                  </Button>
                )
              }
            />
          ) : (
            <div className="flex flex-wrap items-center gap-3 rounded-md border border-border bg-muted/40 p-4 text-sm text-muted-foreground">
              <span className="min-w-0 flex-1 font-medium">Checks the bot token with {service}.</span>
              <Button size="sm" variant="test" onClick={() => void runTest()}>
                Test connection
              </Button>
            </div>
          )}
        </Panel>

        <Panel title="Rotate bot token">
          <div className="space-y-3">
            <CredentialField
              label="Bot token"
              mode={tokenMode}
              onModeChange={(m) => {
                setTokenMode(m)
                if (m === 'existing' && !existingRef) setExistingRef(channel.credential_ref)
              }}
              existingRef={existingRef}
              onExistingRefChange={setExistingRef}
              secretValue={token}
              onSecretValueChange={setToken}
              secretPlaceholder={slack ? 'xoxb-...' : '123456:ABC-DEF...'}
              defaultBackend={defaultBackend}
              refName={channel.credential_ref}
              modeLabels={{ new: 'New token', existing: 'Different credential' }}
            />
            <Button
              size="sm"
              disabled={savingToken || (usingExisting ? !existingRef : !token.trim())}
              onClick={() => void saveToken()}
            >
              {savingToken ? 'Saving…' : 'Save token'}
            </Button>
          </div>
        </Panel>

        {slack && (
          <Panel title="Rotate app token">
            <div className="space-y-3">
              <CredentialField
                label="App token"
                mode={appTokenMode}
                onModeChange={(m) => {
                  setAppTokenMode(m)
                  if (m === 'existing' && !existingAppRef) setExistingAppRef(channel.config.app_token_ref ?? '')
                }}
                existingRef={existingAppRef}
                onExistingRefChange={setExistingAppRef}
                secretValue={appToken}
                onSecretValueChange={setAppToken}
                secretPlaceholder="xapp-..."
                defaultBackend={defaultBackend}
                refName={channel.config.app_token_ref ?? ''}
                modeLabels={{ new: 'New token', existing: 'Different credential' }}
              />
              <Button
                size="sm"
                disabled={savingAppToken || (usingExistingApp ? !existingAppRef : !appToken.trim())}
                onClick={() => void saveAppToken()}
              >
                {savingAppToken ? 'Saving…' : 'Save app token'}
              </Button>
            </div>
          </Panel>
        )}
      </div>

      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title={`Delete ${channel.name}?`}
        description="Stops the bot and removes its pairings. Chat sessions it started stay in your history."
        confirmLabel="Delete"
        destructive
        onConfirm={() => void remove()}
      />
    </PageShell>
  )
}
