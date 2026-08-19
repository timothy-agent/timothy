import { Route, Routes } from 'react-router'
import { DestinationAdd } from './DestinationAdd'
import { DestinationEdit } from './DestinationEdit'
import { DestinationsList } from './DestinationsList'

// DestinationsTab is the route container for the destinations area: a
// list screen, a dedicated add screen, and a dedicated manage screen —
// mirrors ConnectorsTab's file split.
export function DestinationsTab() {
  return (
    <Routes>
      <Route path="/" element={<DestinationsList />} />
      <Route path="new/:kind" element={<DestinationAdd />} />
      <Route path=":id" element={<DestinationEdit />} />
    </Routes>
  )
}
