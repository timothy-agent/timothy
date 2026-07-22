import { Route, Routes } from 'react-router'
import { AgentAdd } from './AgentAdd'
import { AgentEdit } from './AgentEdit'
import { AgentsList } from './AgentsList'

// AgentsTab is the route container for the agents area: a list screen,
// a dedicated add screen, and a dedicated manage screen.
export function AgentsTab() {
  return (
    <Routes>
      <Route path="/" element={<AgentsList />} />
      <Route path="new" element={<AgentAdd />} />
      <Route path=":id" element={<AgentEdit />} />
    </Routes>
  )
}
