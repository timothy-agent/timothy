import { Route, Routes } from 'react-router'
import { KnowledgeAutoAdd } from '../components/knowledge/KnowledgeAutoAdd'
import { KnowledgeCollectionAdd } from '../components/knowledge/KnowledgeCollectionAdd'
import { KnowledgeCollectionDetail } from '../components/knowledge/KnowledgeCollectionDetail'
import { KnowledgeCollectionsList } from '../components/knowledge/KnowledgeCollectionsList'
import { PageShell } from '../components/timothy/page-shell'

export function Knowledge() {
  return (
    <PageShell>
      <Routes>
        <Route path="/" element={<KnowledgeCollectionsList />} />
        <Route path="new" element={<KnowledgeCollectionAdd />} />
        <Route path="add" element={<KnowledgeAutoAdd />} />
        <Route path=":id" element={<KnowledgeCollectionDetail />} />
      </Routes>
    </PageShell>
  )
}
