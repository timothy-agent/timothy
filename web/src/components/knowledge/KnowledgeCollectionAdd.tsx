import { useState } from 'react'
import { useNavigate } from 'react-router'
import { toast } from 'sonner'
import { createKbCollection } from '../../api/client'
import { Field } from '../timothy/field'
import { PageHeader } from '../timothy/page-header'
import { Input } from '../ui/input'
import { Button } from '../ui/button'
import { errText } from '../../lib/errors'
import { slugify } from '../../lib/slugify'

export function KnowledgeCollectionAdd() {
  const navigate = useNavigate()
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async () => {
    setBusy(true)
    try {
      const id = await createKbCollection({ name: slugify(name), description: description.trim() })
      toast.success('Collection created', { description: `${slugify(name)} is ready for documents.` })
      navigate(`/knowledge/${id}`)
    } catch (err) {
      toast.error('Could not create collection', { description: errText(err) })
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="w-full space-y-6">
      <PageHeader
        title="New collection"
        description="A named group of documents agents can search with search_kb."
        breadcrumbs={[{ label: 'Knowledge', href: '/knowledge' }, { label: 'New collection' }]}
      />

      <div className="max-w-3xl">
        <div className="grid gap-5">
          <Field label="Name" description="unique slug, immutable after creation">
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="product-docs, runbooks…"
              className="mt-1.5 h-10"
            />
          </Field>
          <Field label="Description" description="what this collection covers">
            <Input
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="What documents live here"
              className="mt-1.5 h-10"
            />
          </Field>
        </div>

        <div className="flex gap-3 pt-6">
          <Button variant="outline" disabled={busy} onClick={() => navigate('/knowledge')}>
            Cancel
          </Button>
          <Button disabled={slugify(name) === '' || busy} onClick={() => void submit()}>
            Create collection
          </Button>
        </div>
      </div>
    </div>
  )
}
