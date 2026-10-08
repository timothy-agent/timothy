import { ChevronDownIcon } from 'lucide-react'
import { useEffect, useState } from 'react'
import { toast } from 'sonner'
import { getSettings, patchSettingValues } from '../../api/client'
import { listTimezones } from '../../lib/timezones'
import { Field } from '../../components/timothy/field'
import { Button } from '../../components/ui/button'
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from '../../components/ui/command'
import { Popover, PopoverContent, PopoverTrigger } from '../../components/ui/popover'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../../components/ui/select'
import { CURRENCIES } from '../../lib/currencies'
import { errText } from '../../lib/errors'

export function BasicsStep({ onBack, onNext }: { onBack: () => void; onNext: () => void }) {
  const [timezone, setTimezone] = useState('')
  const [currency, setCurrency] = useState('USD')
  const [zones] = useState(listTimezones)
  const [open, setOpen] = useState(false)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    let active = true
    getSettings().then(
      ({ values }) => {
        if (!active) return
        setTimezone(values.timezone || Intl.DateTimeFormat().resolvedOptions().timeZone)
        if (values.default_currency) setCurrency(values.default_currency)
      },
      () => {
        if (active) setTimezone(Intl.DateTimeFormat().resolvedOptions().timeZone)
      },
    )
    return () => {
      active = false
    }
  }, [])

  const save = async () => {
    setBusy(true)
    try {
      await patchSettingValues({ timezone, default_currency: currency })
      onNext()
    } catch (err) {
      toast.error('Could not save', { description: errText(err) })
    } finally {
      setBusy(false)
    }
  }

  return (
    <div>
      <h1 className="text-xl font-semibold">A few basics</h1>
      <p className="mt-2 text-sm text-muted-foreground">
        Timothy shows dates in your timezone and plans mission budgets in your currency.
      </p>
      <div className="mt-6 space-y-5">
        <Field label="Timezone">
          {(props) => (
            <Popover open={open} onOpenChange={setOpen}>
              <PopoverTrigger asChild>
                <Button
                  id={props.id}
                  variant="outline"
                  role="combobox"
                  aria-expanded={open}
                  aria-label="Timezone"
                  className="h-9 w-64 justify-between font-normal"
                >
                  <span className="truncate text-left">{timezone || 'UTC (default)'}</span>
                  <ChevronDownIcon className="size-4 shrink-0 opacity-50" />
                </Button>
              </PopoverTrigger>
              <PopoverContent className="w-[min(92vw,20rem)] p-0" align="start">
                <Command>
                  <CommandInput placeholder="Search timezones..." />
                  <CommandList>
                    <CommandEmpty>No timezone matches.</CommandEmpty>
                    <CommandGroup>
                      {zones.map((z) => (
                        <CommandItem
                          key={z}
                          value={z}
                          data-checked={timezone === z || undefined}
                          onSelect={() => {
                            setTimezone(z)
                            setOpen(false)
                          }}
                        >
                          {z}
                        </CommandItem>
                      ))}
                    </CommandGroup>
                  </CommandList>
                </Command>
              </PopoverContent>
            </Popover>
          )}
        </Field>
        <Field label="Default currency">
          {(props) => (
            <Select value={currency} onValueChange={setCurrency}>
              <SelectTrigger id={props.id} className="h-9 w-64" aria-label="Default currency">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {CURRENCIES.map((c) => (
                  <SelectItem key={c} value={c}>
                    {c}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
        </Field>
      </div>
      <div className="mt-8 flex flex-wrap items-center gap-2">
        <Button variant="outline" disabled={busy} onClick={onBack}>
          Back
        </Button>
        <Button disabled={busy} onClick={() => void save()}>
          Save and continue
        </Button>
        <Button variant="link" disabled={busy} onClick={onNext}>
          Skip this step
        </Button>
      </div>
    </div>
  )
}
