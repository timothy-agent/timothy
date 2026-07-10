import { useState } from 'react'
import { getToken, setToken } from '../api/client'
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

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Settings</DialogTitle>
          <DialogDescription>
            The API token authenticates this browser against your Timothy instance. It is stored
            locally, never sent anywhere else.
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
