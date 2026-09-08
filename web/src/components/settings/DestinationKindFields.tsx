import type { AdminConnector } from '../../api/types'
import {
  COMMIT_STYLE_DEFAULT,
  ON_COMPLETE_NONE,
  commitStyleChoices,
  onCompleteChoices,
} from '../../lib/githubDestination'
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
  mode: 'push' | 'push_pr'
  branchPattern: string
  commitStyle: string
  createIfMissing: boolean
}

// DestinationKindFields renders the labelled fields for one destination
// kind, shared by DestinationAdd and DestinationEdit so the field set
// lives once. Every Select is labelled (contract 10.1). Bot token
// belongs to the caller (Add: new secret; Edit: independent rotation
// Panel), so telegram only contributes the chat id here.
export function DestinationKindFields({
  kind,
  values,
  setField,
  connectors,
}: {
  kind: 'email' | 'webhook' | 'telegram' | 'github'
  values: DestinationKindValues
  setField: <K extends keyof DestinationKindValues>(key: K, value: DestinationKindValues[K]) => void
  connectors: AdminConnector[]
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

  if (kind === 'telegram') {
    return (
      <Field label="Chat ID" description="the numeric chat or channel id the bot posts to">
        <Input value={values.chatID} onChange={(e) => setField('chatID', e.target.value)} placeholder="123456789" />
      </Field>
    )
  }

  return (
    <>
      <Field label="GitHub connector">
        {(props) => (
          <Select value={values.connectorID} onValueChange={(v) => setField('connectorID', v)}>
            <SelectTrigger id={props.id} className="w-full" aria-label="GitHub connector">
              <SelectValue placeholder="Choose a connected GitHub account" />
            </SelectTrigger>
            <SelectContent>
              {connectors
                .filter((c) => c.kind === 'github')
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
