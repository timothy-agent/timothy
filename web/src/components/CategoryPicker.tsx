import { categories } from '../lib/chat'
import { Listbox, ListboxLabel, ListboxOption } from './catalyst/listbox'

export function CategoryPicker({
  value,
  onChange,
}: {
  value: string
  onChange: (v: string) => void
}) {
  return (
    <Listbox
      aria-label="Task category"
      name="category"
      value={value}
      onChange={onChange}
      className="w-40"
    >
      {categories.map((c) => (
        <ListboxOption key={c} value={c}>
          <ListboxLabel>{c}</ListboxLabel>
        </ListboxOption>
      ))}
    </Listbox>
  )
}
