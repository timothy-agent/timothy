import { useNavigate } from 'react-router'
import { toast } from 'sonner'
import { addKbDocumentFromUrlAuto, uploadKbDocumentAuto } from '../../api/client'
import { PageHeader } from '../timothy/page-header'
import { KbUploadForm } from './KbUploadForm'

// KnowledgeAutoAdd is the top-level "Add to Knowledgebase" entry point:
// no collection is chosen here — brain classifies each upload against
// existing collections (or creates a new one) and files it there. On
// success, jump to the collection the document landed in so the user
// sees where it went.
export function KnowledgeAutoAdd() {
  const navigate = useNavigate()

  return (
    <div className="space-y-6">
      <PageHeader
        title="Add to Knowledgebase"
        description="Drop a file or paste a URL — it's classified into the best matching collection automatically, or a new one is created if nothing fits."
        breadcrumbs={[{ label: 'Knowledge', href: '/knowledge' }, { label: 'Add to Knowledgebase' }]}
      />

      <KbUploadForm
        uploadFile={uploadKbDocumentAuto}
        addUrl={addKbDocumentFromUrlAuto}
        onUploaded={(doc) => {
          toast.success(`${doc.title} added`)
          navigate(`/knowledge/${doc.collection_id}`)
        }}
      />
    </div>
  )
}
