import { ArrowLeft01Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { toast } from 'sonner'
import { createKbCollection } from '../../api/client'
import { slugify } from '../settings/AgentForm'
import { Field } from '../settings/shared'
import { Input } from '../ui/input'
import { Button } from '../ui/button'
import { errText } from '../settings/util'

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
    <div className="mt-6 w-full space-y-6">
      <Link
        to="/knowledge"
        className="inline-flex w-fit items-center gap-1.5 text-sm text-muted-foreground transition hover:text-foreground"
      >
        <HugeiconsIcon icon={ArrowLeft01Icon} className="size-4" />
        Knowledge
      </Link>

      <div className="border-b border-border pb-6">
        <h1 className="text-xl font-semibold tracking-tight">New collection</h1>
        <p className="text-sm text-muted-foreground">
          A named group of documents agents can search with kb_search.
        </p>
      </div>

      <div className="max-w-3xl">
        <div className="grid gap-5">
          <Field label="Name" hint="unique slug, immutable after creation">
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="product-docs, runbooks…"
              className="mt-1.5 h-10"
            />
          </Field>
          <Field label="Description" hint="what this collection covers">
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
