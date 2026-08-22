import { Route, Routes } from 'react-router'
import { KnowledgeAutoAdd } from '../components/knowledge/KnowledgeAutoAdd'
import { KnowledgeCollectionAdd } from '../components/knowledge/KnowledgeCollectionAdd'
import { KnowledgeCollectionDetail } from '../components/knowledge/KnowledgeCollectionDetail'
import { KnowledgeCollectionsList } from '../components/knowledge/KnowledgeCollectionsList'

export function Knowledge() {
  return (
    <div className="h-full overflow-y-auto">
      <div className="mx-auto w-full max-w-full space-y-6 p-6">
        <h1 className="text-lg font-semibold">Knowledge</h1>
        <Routes>
          <Route path="/" element={<KnowledgeCollectionsList />} />
          <Route path="new" element={<KnowledgeCollectionAdd />} />
          <Route path="add" element={<KnowledgeAutoAdd />} />
          <Route path=":id" element={<KnowledgeCollectionDetail />} />
        </Routes>
      </div>
    </div>
  )
}
