import { Link } from 'react-router'
import type { AdminConnector } from '../../api/types'
import { ChipsInput } from '../automations/TriggerFields'
import { Field } from '../timothy/field'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../ui/select'
import { maxFromAllow } from './channelEmail'

// ChannelEmailFields edits an email channel's IMAP connector and sender
// allowlist.
export function ChannelEmailFields({
  connectors,
  connectorID,
  onConnectorChange,
  fromAllow,
  onFromAllowChange,
}: {
  connectors: AdminConnector[] | null
  connectorID: string
  onConnectorChange: (id: string) => void
  fromAllow: string[]
  onFromAllowChange: (next: string[]) => void
}) {
  return (
    <>
      <Field label="IMAP connector" description="the mailbox Timothy reads and replies from">
        {(props) =>
          connectors && connectors.length === 0 ? (
            <p className="text-sm text-muted-foreground">
              <Link to="/settings/connectors" className="underline underline-offset-4">
                Add an IMAP connector with SMTP first
              </Link>
            </p>
          ) : (
            // Radix echoes "" when the value is set before its options
            // mount; that is never a pick.
            <Select value={connectorID} onValueChange={(v) => v && onConnectorChange(v)}>
              <SelectTrigger id={props.id} className="w-full" aria-label="IMAP connector">
                <SelectValue placeholder="Choose a connector" />
              </SelectTrigger>
              <SelectContent>
                {(connectors ?? []).map((c) => (
                  <SelectItem key={c.id} value={c.id}>
                    {c.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )
        }
      </Field>
      <Field label="From allowlist" description="addresses or @domain; mail from anyone else is ignored">
        {(p) => (
          <ChipsInput
            {...p}
            value={fromAllow}
            onChange={(next) => onFromAllowChange([...new Set(next.map((v) => v.toLowerCase()))])}
            max={maxFromAllow}
            split={/[,\s]+/}
            itemLabel="sender"
            placeholder="you@example.com or @example.com"
          />
        )}
      </Field>
    </>
  )
}
