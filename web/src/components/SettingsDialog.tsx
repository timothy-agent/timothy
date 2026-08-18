import { useEffect, useState } from 'react'
import { acknowledgeNeedToken, getToken, setToken } from '../api/client'
import { Button } from './ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from './ui/dialog'
import { Input } from './ui/input'
import { Label } from './ui/label'

export function SettingsDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const [value, setValue] = useState(getToken())

  // The component stays mounted across opens, so discard any unsaved
  // edits from the previous open.
  useEffect(() => {
    if (open) setValue(getToken())
  }, [open])

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Settings</DialogTitle>
          <DialogDescription>
            Paste the TIMOTHY_API_TOKEN from deploy/.env. This authenticates the browser against
            your Timothy instance — it is not an LLM provider API key.
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-2">
          <Label htmlFor="token">API token</Label>
          <Input
            id="token"
            type="password"
            name="token"
            value={value}
            onChange={(e) => setValue(e.target.value)}
            placeholder="TIMOTHY_API_TOKEN value"
          />
        </div>
        <DialogFooter>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            onClick={() => {
              setToken(value)
              acknowledgeNeedToken()
              onClose()
            }}
          >
            Save
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
