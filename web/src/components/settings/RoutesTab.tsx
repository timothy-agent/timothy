import { Route, Routes } from 'react-router'
import { RouteEdit } from './RouteEdit'
import { RoutesList } from './RoutesList'

// RoutesTab is the route container for the routes area. Routes are
// fully user-managed (create, rename via chain editing, delete) — the
// list shows them as cards plus the 4 required system roles, and each
// route opens its own chain-editing page.
export function RoutesTab() {
  return (
    <Routes>
      <Route path="/" element={<RoutesList />} />
      <Route path=":name" element={<RouteEdit />} />
    </Routes>
  )
}
