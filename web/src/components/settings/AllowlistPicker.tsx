import { Field } from '../timothy/field'
import { Input } from '../ui/input'
import { Combobox, type ComboboxOption } from '../timothy/combobox'
import { useCachedList } from './useCachedList'

interface AllowlistItem {
  id: string
  label: string
  description?: string
}

// AllowlistPicker builds an agent's skills/tools/knowledge allowlist
// from an actual fetched list instead of typed blind, the exact
// name (a skill pack, a connector tool, a collection) is otherwise
// undiscoverable. Falls back to a labelled comma-separated free-text
// input while the list is empty or failed to load, so the allowlist
// stays usable either way (10.9).
export function AllowlistPicker({
  label,
  description,
  value,
  onChange,
  load,
  cacheKey,
  emptyText,
  freeTextPlaceholder,
}: {
  label: string
  description?: string
  value: string[]
  onChange: (v: string[]) => void
  load: () => Promise<AllowlistItem[]>
  cacheKey: string
  emptyText: string
  freeTextPlaceholder: string
}) {
  const { items, failed } = useCachedList(cacheKey, load)

  if (items.length === 0 || failed) {
    return (
      <Field label={label} description={description}>
        {(props) => (
          <Input
            {...props}
            value={value.join(', ')}
            onChange={(e) =>
              onChange(
                e.target.value
                  .split(',')
                  .map((s) => s.trim())
                  .filter(Boolean),
              )
            }
            placeholder={freeTextPlaceholder}
          />
        )}
      </Field>
    )
  }

  const options: ComboboxOption[] = items.map((item) => ({
    value: item.id,
    label: item.label,
    description: item.description,
  }))

  return (
    <Field label={label} description={description}>
      {(props) => (
        <Combobox
          id={props.id}
          multiple
          options={options}
          value={value}
          onChange={onChange}
          emptyText={emptyText}
          aria-label={label}
        />
      )}
    </Field>
  )
}
