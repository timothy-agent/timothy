import { Route, Routes } from 'react-router'
import { KnowledgeCollectionAdd } from './KnowledgeCollectionAdd'
import { KnowledgeCollectionDetail } from './KnowledgeCollectionDetail'
import { KnowledgeCollectionsList } from './KnowledgeCollectionsList'

// KnowledgeTab is the route container for the knowledge area: a
// collections list, a dedicated create screen, and a detail screen
// for documents within one collection — mirrors AgentsTab.
export function KnowledgeTab() {
  return (
    <Routes>
      <Route path="/" element={<KnowledgeCollectionsList />} />
      <Route path="new" element={<KnowledgeCollectionAdd />} />
      <Route path=":id" element={<KnowledgeCollectionDetail />} />
    </Routes>
  )
}
