import { useState } from 'react'
import { Button } from './catalyst/button'
import { Dialog, DialogActions, DialogBody, DialogDescription, DialogTitle } from './catalyst/dialog'
import { Field, Label } from './catalyst/fieldset'
import { Input } from './catalyst/input'
import { getToken, setToken } from '../api/client'

export function SettingsDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const [value, setValue] = useState(getToken())

  return (
    <Dialog open={open} onClose={onClose}>
      <DialogTitle>Settings</DialogTitle>
      <DialogDescription>
        The API token authenticates this browser against your Timothy instance. It is stored
        locally, never sent anywhere else.
      </DialogDescription>
      <DialogBody>
        <Field>
          <Label>API token</Label>
          <Input
            type="password"
            name="token"
            value={value}
            onChange={(e) => setValue(e.target.value)}
            placeholder="TIMOTHY_API_TOKEN value"
          />
        </Field>
      </DialogBody>
      <DialogActions>
        <Button plain onClick={onClose}>
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
      </DialogActions>
    </Dialog>
  )
}
