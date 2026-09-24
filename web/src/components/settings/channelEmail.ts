import type { AdminConnector } from '../../api/types'

// maxFromAllow mirrors the server's email allowlist cap.
export const maxFromAllow = 50

// emailConnectors keeps the connectors an email channel can use:
// enabled IMAP connectors that can reply over SMTP.
export function emailConnectors(rows: AdminConnector[]): AdminConnector[] {
  return rows.filter((c) => c.kind === 'imap' && c.enabled && typeof c.config.smtp_host === 'string' && c.config.smtp_host !== '')
}
