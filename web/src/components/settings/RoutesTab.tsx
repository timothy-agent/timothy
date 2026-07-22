import { Route, Routes } from 'react-router'
import { RouteEdit } from './RouteEdit'
import { RoutesList } from './RoutesList'

// RoutesTab is the route container for the routes area. Routes are a
// fixed, backend-defined set (no create/delete) — the list shows them
// as cards, and each opens its own chain-editing page.
export function RoutesTab() {
  return (
    <Routes>
      <Route path="/" element={<RoutesList />} />
      <Route path=":name" element={<RouteEdit />} />
    </Routes>
  )
}
