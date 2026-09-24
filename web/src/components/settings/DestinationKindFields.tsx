import type { AdminConnector, Channel } from '../../api/types'
import {
  COMMIT_STYLE_DEFAULT,
  ON_COMPLETE_NONE,
  commitStyleChoices,
  onCompleteChoices,
} from '../../lib/githubDestination'
import { gitKindMeta, type GitKind } from '../../lib/gitKinds'
import { Checkbox } from '../ui/checkbox'
import { Input } from '../ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../ui/select'
import { Field } from '../timothy/field'

export interface DestinationKindValues {
  connectorID: string
  to: string
  url: string
  format: 'json' | 'text'
  chatID: string
  threadID: string
  channelID: string
  mode: 'push' | 'push_pr'
  branchPattern: string
  commitStyle: string
  createIfMissing: boolean
}

// chatHint is the chat id hint per channel kind.
const chatHint: Record<Channel['kind'], string> = {
  telegram: 'Telegram: the chat id of a group or your own chat',
  slack: 'Slack: the channel id such as C0123',
  email: 'Email: the recipient',
}

// DestinationKindFields renders the labelled fields for one destination
// kind, shared by DestinationAdd and DestinationEdit so the field set
// lives once. Every Select is labelled (contract 10.1). A channel
// destination carries no credential: the channel holds it.
export function DestinationKindFields({
  kind,
  values,
  setField,
  connectors,
  channels = [],
}: {
  kind: 'email' | 'webhook' | 'channel' | GitKind
  values: DestinationKindValues
  setField: <K extends keyof DestinationKindValues>(key: K, value: DestinationKindValues[K]) => void
  connectors: AdminConnector[]
  channels?: Channel[]
}) {
  if (kind === 'email') {
    return (
      <>
        <Field label="Google connector">
          {(props) => (
            <Select value={values.connectorID} onValueChange={(v) => setField('connectorID', v)}>
              <SelectTrigger id={props.id} className="w-full" aria-label="Google connector">
                <SelectValue placeholder="Choose a connected Gmail account" />
              </SelectTrigger>
              <SelectContent>
                {connectors
                  .filter((c) => c.kind === 'google')
                  .map((c) => (
                    <SelectItem key={c.id} value={c.id}>
                      {c.name}
                    </SelectItem>
                  ))}
              </SelectContent>
            </Select>
          )}
        </Field>
        <Field label="To" description="recipient address">
          <Input value={values.to} onChange={(e) => setField('to', e.target.value)} placeholder="ops@example.com" />
        </Field>
      </>
    )
  }

  if (kind === 'webhook') {
    return (
      <>
        <Field label="URL">
          <Input value={values.url} onChange={(e) => setField('url', e.target.value)} placeholder="https://…/hook" />
        </Field>
        <Field label="Format">
          {(props) => (
            <Select value={values.format} onValueChange={(v) => setField('format', v as 'json' | 'text')}>
              <SelectTrigger id={props.id} className="w-full" aria-label="Format">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="json">JSON</SelectItem>
                <SelectItem value="text">Plain text</SelectItem>
              </SelectContent>
            </Select>
          )}
        </Field>
      </>
    )
  }

  if (kind === 'channel') {
    const picked = channels.find((c) => c.id === values.channelID)
    return (
      <>
        <Field label="Channel">
          {(props) => (
            <Select value={values.channelID} onValueChange={(v) => setField('channelID', v)}>
              <SelectTrigger id={props.id} className="w-full" aria-label="Channel">
                <SelectValue placeholder="Choose a channel" />
              </SelectTrigger>
              <SelectContent>
                {channels.map((c) => (
                  <SelectItem key={c.id} value={c.id}>
                    {c.name} ({c.kind})
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
        </Field>
        {picked?.kind === 'email' && (
          <Field label="To" description={chatHint.email}>
            <Input value={values.to} onChange={(e) => setField('to', e.target.value)} placeholder="ops@example.com" />
          </Field>
        )}
        {picked && picked.kind !== 'email' && (
          <>
            <Field label="Chat ID" description={chatHint[picked.kind]}>
              <Input
                value={values.chatID}
                onChange={(e) => setField('chatID', e.target.value)}
                placeholder={picked.kind === 'slack' ? 'C0123' : '-1001234567890'}
                className="font-mono"
              />
            </Field>
            <Field
              label="Thread ID"
              description={picked.kind === 'slack' ? 'Slack: a thread ts to reply under' : 'Telegram: a forum topic id'}
              optional
            >
              <Input
                value={values.threadID}
                onChange={(e) => setField('threadID', e.target.value)}
                className="font-mono"
              />
            </Field>
          </>
        )}
      </>
    )
  }

  const connectorLabel = `${gitKindMeta(kind).label} connector`

  return (
    <>
      <Field label={connectorLabel}>
        {(props) => (
          <Select value={values.connectorID} onValueChange={(v) => setField('connectorID', v)}>
            <SelectTrigger id={props.id} className="w-full" aria-label={connectorLabel}>
              <SelectValue placeholder={`Choose a connected ${gitKindMeta(kind).label} account`} />
            </SelectTrigger>
            <SelectContent>
              {connectors
                .filter((c) => c.kind === kind)
                .map((c) => (
                  <SelectItem key={c.id} value={c.id}>
                    {c.name}
                  </SelectItem>
                ))}
            </SelectContent>
          </Select>
        )}
      </Field>
      <Field label="Mode">
        {(props) => (
          <Select value={values.mode} onValueChange={(v) => setField('mode', v as 'push' | 'push_pr')}>
            <SelectTrigger id={props.id} className="w-full" aria-label="Mode">
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
        )}
      </Field>
      <Field label="Branch pattern" description="optional" required={false}>
        <Input
          value={values.branchPattern}
          onChange={(e) => setField('branchPattern', e.target.value)}
          placeholder="Default (from settings)"
        />
      </Field>
      <Field label="Commit style">
        {(props) => (
          <Select
            value={values.commitStyle || COMMIT_STYLE_DEFAULT}
            onValueChange={(v) => setField('commitStyle', v === COMMIT_STYLE_DEFAULT ? '' : v)}
          >
            <SelectTrigger id={props.id} className="w-full" aria-label="Commit style">
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
        )}
      </Field>
      <Field label="Create repository if missing" required={false}>
        {(props) => (
          <div className="flex items-center gap-3">
            <Checkbox
              id={props.id}
              checked={values.createIfMissing}
              onCheckedChange={(v) => setField('createIfMissing', v === true)}
            />
            <span className="text-sm text-muted-foreground">
              Create the repository through this connector when the mission has no target repository.
            </span>
          </div>
        )}
      </Field>
    </>
  )
}
