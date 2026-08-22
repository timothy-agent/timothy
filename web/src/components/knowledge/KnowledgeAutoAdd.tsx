import { ArrowLeft01Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { Link, useNavigate } from 'react-router'
import { toast } from 'sonner'
import { addKbDocumentFromUrlAuto, uploadKbDocumentAuto } from '../../api/client'
import { KbUploadForm } from './KbUploadForm'

// KnowledgeAutoAdd is the top-level "Add to Knowledgebase" entry point:
// no collection is chosen here — brain classifies each upload against
// existing collections (or creates a new one) and files it there. On
// success, jump to the collection the document landed in so the user
// sees where it went.
export function KnowledgeAutoAdd() {
  const navigate = useNavigate()

  return (
    <div className="mt-6 space-y-6">
      <Link
        to="/knowledge"
        className="inline-flex w-fit items-center gap-1.5 text-sm text-muted-foreground transition hover:text-foreground"
      >
        <HugeiconsIcon icon={ArrowLeft01Icon} className="size-4" />
        Knowledge
      </Link>

      <div>
        <h2 className="text-sm font-semibold">Add to Knowledgebase</h2>
        <p className="text-sm text-muted-foreground">
          Drop a file or paste a URL — it's classified into the best matching collection
          automatically, or a new one is created if nothing fits.
        </p>
      </div>

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
