import { Route, Routes } from 'react-router'
import { ConnectorAdd } from './ConnectorAdd'
import { ConnectorEdit } from './ConnectorEdit'
import { ConnectorsList } from './ConnectorsList'

// ConnectorsTab is the route container for the connectors area: a list
// screen, a dedicated add screen, and a dedicated manage screen — both
// create and edit get their own page, not a dialog.
export function ConnectorsTab() {
  return (
    <Routes>
      <Route path="/" element={<ConnectorsList />} />
      <Route path="new/:presetId" element={<ConnectorAdd />} />
      <Route path=":id" element={<ConnectorEdit />} />
    </Routes>
  )
}
