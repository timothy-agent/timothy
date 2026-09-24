import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router'
import { toast } from 'sonner'
import { createChannel, listAgents, patchChannel, setSecret, testChannel } from '../../api/client'
import type { AdminAgent, Channel } from '../../api/types'
import { Button } from '../ui/button'
import { Input } from '../ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../ui/select'
import { PageHeader } from '../timothy/page-header'
import { PageShell } from '../timothy/page-shell'
import { Field, FieldGroup, Form, FormActions } from '../timothy/field'
import { ChannelAgentSelect } from './ChannelAgentSelect'
import { channelAgentPatch, DEFAULT_AGENT } from './channelAgent'
import { CredentialField, type CredentialMode } from './CredentialRefPicker'
import { settingsArea } from './settingsAreas'
import { TestStatus } from './TestStatus'
import { useDefaultSecretBackend } from './useDefaultSecretBackend'
import { errText } from '../../lib/errors'
import { slugify } from '../../lib/slugify'

const area = settingsArea('channels')
const title = 'New channel'

// ChannelAdd creates the channel disabled while testing its bot token;
// a passing test enables it on Add, same flow as DestinationAdd. A
// retry after a failed test updates the saved row instead of creating
// a second one.
export function ChannelAdd() {
  const navigate = useNavigate()
  const defaultBackend = useDefaultSecretBackend()
  const [agents, setAgents] = useState<AdminAgent[]>([])
  const [name, setName] = useState('')
  const [kind, setKind] = useState<Channel['kind']>('telegram')
  const [agent, setAgent] = useState(DEFAULT_AGENT)
  const [token, setToken] = useState('')
  const [tokenMode, setTokenMode] = useState<CredentialMode>('new')
  const [existingRef, setExistingRef] = useState('')
  const [busy, setBusy] = useState(false)
  const [createdID, setCreatedID] = useState<string | null>(null)
  const [test, setTest] = useState<{ ok: boolean; message: string } | null>(null)

  useEffect(() => {
    listAgents()
      .then(setAgents)
      .catch((err: unknown) => toast.error('Could not load agents', { description: errText(err) }))
  }, [])

  const usingExisting = tokenMode === 'existing'
  const tokenRef = usingExisting ? existingRef : `${slugify(name).toUpperCase().replace(/-/g, '_')}_CHANNEL_BOT_TOKEN`
  const canTest = name.trim() !== '' && (usingExisting ? existingRef !== '' : token.trim() !== '')

  const edit = <T,>(set: (v: T) => void) => (v: T) => {
    set(v)
    setTest(null)
  }

  const runTest = async () => {
    setBusy(true)
    setTest(null)
    try {
      if (!usingExisting) await setSecret(tokenRef, token.trim())
      const { agent_id, config } = channelAgentPatch(agent)
      let id = createdID
      if (id) {
        await patchChannel(id, { name: name.trim(), credential_ref: tokenRef, agent_id, config })
      } else {
        id = await createChannel({
          name: name.trim(),
          kind,
          credential_ref: tokenRef,
          agent_id: agent_id ?? undefined,
          config,
          enabled: false,
        })
        setCreatedID(id)
      }
      const res = await testChannel(id)
      setTest({ ok: true, message: `Connected as @${res.bot_username}, ready to add.` })
    } catch (err) {
      setTest({ ok: false, message: `Test failed: ${errText(err)}. Fix and retry.` })
    } finally {
      setBusy(false)
    }
  }

  const submit = async () => {
    if (!test?.ok || !createdID) return
    setBusy(true)
    try {
      await patchChannel(createdID, { enabled: true })
      toast.success('Channel added')
      navigate(`/settings/channels/${createdID}`)
    } catch (err) {
      toast.error('Could not enable channel', { description: errText(err) })
    } finally {
      setBusy(false)
    }
  }

  return (
    <PageShell width="form">
      <PageHeader
        title={title}
        description="Telegram bot, long polling"
        breadcrumbs={[
          { label: 'Settings', href: '/settings' },
          { label: area.label, href: '/settings/channels' },
          { label: title },
        ]}
      />
      <Form onSubmit={(e) => e.preventDefault()}>
        <FieldGroup>
          <Field label="Name" description="unique">
            <Input value={name} onChange={(e) => edit(setName)(e.target.value)} placeholder="my-telegram" />
          </Field>
          <Field label="Kind">
            <Select value={kind} onValueChange={(v) => edit(setKind)(v as Channel['kind'])}>
              <SelectTrigger className="w-full" aria-label="Kind">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="telegram">Telegram</SelectItem>
                <SelectItem value="slack" disabled>
                  Slack (coming later)
                </SelectItem>
                <SelectItem value="email" disabled>
                  Email (coming later)
                </SelectItem>
              </SelectContent>
            </Select>
          </Field>
          <CredentialField
            label="Bot token"
            mode={tokenMode}
            onModeChange={edit(setTokenMode)}
            existingRef={existingRef}
            onExistingRefChange={edit(setExistingRef)}
            secretValue={token}
            onSecretValueChange={edit(setToken)}
            secretPlaceholder="123456:ABC-DEF..."
            defaultBackend={defaultBackend}
            refName={tokenRef}
          />
          <Field label="Agent" description="who answers new conversations">
            <ChannelAgentSelect value={agent} onChange={edit(setAgent)} agents={agents} />
          </Field>
          <p className="text-sm text-muted-foreground">
            Timothy long-polls Telegram for this bot, so nothing inbound needs exposing. Create the bot with
            @BotFather and paste its token here.
          </p>
        </FieldGroup>

        {test || busy ? (
          <TestStatus
            state={busy ? 'testing' : test?.ok ? 'ok' : 'failed'}
            message={busy ? 'Testing…' : test?.message}
            action={
              !busy && (
                <Button size="sm" variant="test" disabled={!canTest} onClick={() => void runTest()}>
                  Test connection
                </Button>
              )
            }
          />
        ) : (
          <div className="flex flex-wrap items-center gap-3 rounded-md border border-border bg-muted/40 p-4 text-sm text-muted-foreground">
            <span className="min-w-0 flex-1 font-medium">Not tested yet, run a test before adding.</span>
            <Button size="sm" variant="test" disabled={!canTest} onClick={() => void runTest()}>
              Test connection
            </Button>
          </div>
        )}

        <FormActions>
          <Button type="button" variant="outline" disabled={busy} onClick={() => navigate('/settings/channels')}>
            Cancel
          </Button>
          <Button disabled={!test?.ok || busy} onClick={() => void submit()}>
            Add channel
          </Button>
        </FormActions>
      </Form>
    </PageShell>
  )
}
