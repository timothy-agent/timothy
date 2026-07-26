import { useEffect, useState } from 'react'
import { toast } from 'sonner'
import { listConnectors, pushMission, secretStatus } from '../../api/client'
import type { SecretStatus } from '../../api/client'
import { connectorPresets } from '../settings/connectorPresets'
import { backendLabel, errText } from '../settings/util'
import { Button } from '../ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '../ui/dialog'
import { Input } from '../ui/input'
import { Label } from '../ui/label'

const refKey = 'timothy.push_ref'
const githubPreset = connectorPresets.find((p) => p.id === 'github')

export function PushBranchDialog({
  missionId,
  open,
  onOpenChange,
  onPushed,
}: {
  missionId: string
  open: boolean
  onOpenChange: (open: boolean) => void
  onPushed: () => void
}) {
  const [ref, setRef] = useState(() => localStorage.getItem(refKey) ?? '')
  const [status, setStatus] = useState<SecretStatus | null>(null)
  const [busy, setBusy] = useState(false)

  // No remembered ref yet — default to the GitHub MCP connector's
  // credential_ref, if one is configured, so pushing "just works" for
  // the common case of already having connected GitHub.
  useEffect(() => {
    if (localStorage.getItem(refKey) || !githubPreset) return
    listConnectors().then((connectors) => {
      const gh = connectors.find(
        (c) => c.kind === 'mcp' && c.config.endpoint === githubPreset.endpoint,
      )
      if (gh) setRef((current) => current || gh.credential_ref)
    }, () => {})
  }, [])

  useEffect(() => {
    if (!ref.trim()) {
      setStatus(null)
      return
    }
    const t = setTimeout(() => {
      secretStatus(ref.trim()).then(setStatus, () => setStatus(null))
    }, 400)
    return () => clearTimeout(t)
  }, [ref])

  const submit = async () => {
    setBusy(true)
    try {
      const { branch, remote_host } = await pushMission(missionId, ref.trim())
      localStorage.setItem(refKey, ref.trim())
      toast.success(`Pushed ${branch} to ${remote_host}`)
      onOpenChange(false)
      onPushed()
    } catch (err) {
      toast.error('Could not push branch', { description: errText(err) })
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>Push branch</DialogTitle>
          <DialogDescription>
            Pushes the mission&apos;s branch to the repository&apos;s origin remote.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-1.5">
          <Label htmlFor="push-credential-ref">Credential reference</Label>
          <Input
            id="push-credential-ref"
            value={ref}
            onChange={(e) => setRef(e.target.value)}
            placeholder="github_pat"
          />
          {status && (
            <p className="text-xs text-muted-foreground">
              {status.configured
                ? `configured via ${backendLabel(status.backend)}`
                : 'not configured'}
            </p>
          )}
        </div>

        <DialogFooter>
          <Button variant="outline" disabled={busy} onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button disabled={busy || !ref.trim()} onClick={() => void submit()}>
            Push
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
