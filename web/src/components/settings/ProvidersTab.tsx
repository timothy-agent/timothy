import { Route, Routes } from 'react-router'
import { ProviderAdd } from './ProviderAdd'
import { ProviderEdit } from './ProviderEdit'
import { ProvidersList } from './ProvidersList'

// ProvidersTab is the route container for the providers area: a list
// screen, a dedicated add screen, and a dedicated manage screen — both
// create and edit get their own page, not a dialog, so validating a
// provider (a real one-token completion) has room to show its own
// progress without a modal in the way.
export function ProvidersTab() {
  return (
    <Routes>
      <Route path="/" element={<ProvidersList />} />
      <Route path="new/:presetId" element={<ProviderAdd />} />
      <Route path=":id" element={<ProviderEdit />} />
    </Routes>
  )
}
