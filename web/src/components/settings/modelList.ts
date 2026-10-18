import { useEffect, useState } from 'react'
import { availableModels } from '../../api/client'
import type { AvailableModel } from '../../api/types'
import { errText } from '../../lib/errors'
import type { ModelSuggestion } from './ModelPicker'

export interface ModelList {
  models: AvailableModel[]
  // loaded is false until the list for the current id has arrived.
  loaded: boolean
  error: string | null
}

const emptyModelList: ModelList = { models: [], loaded: false, error: null }

// useModelList fetches a provider's one model list (D-172), the source
// every model picker reads. An empty id fetches nothing; a failed fetch
// is an empty list with its error.
export function useModelList(providerId: string): ModelList {
  const [state, setState] = useState<ModelList & { id: string }>({ ...emptyModelList, id: '' })
  useEffect(() => {
    if (!providerId) return
    let cancelled = false
    availableModels(providerId).then(
      (models) => {
        if (!cancelled) setState({ id: providerId, models, loaded: true, error: null })
      },
      (err) => {
        if (!cancelled) setState({ id: providerId, models: [], loaded: true, error: errText(err) })
      },
    )
    return () => {
      cancelled = true
    }
  }, [providerId])
  return state.id === providerId ? state : emptyModelList
}

// listedSuggestions maps a model list to picker rows. An empty list
// offers the provider's default model as the one suggestion.
export function listedSuggestions(models: AvailableModel[], defaultModel?: string): ModelSuggestion[] {
  if (models.length === 0) return defaultModel ? [{ id: defaultModel }] : []
  return models.map((m) => ({
    id: m.id,
    name: m.display_name || undefined,
    input_per_mtok: m.input_per_mtok ?? undefined,
    output_per_mtok: m.output_per_mtok ?? undefined,
  }))
}

// typedIdHint is shown beside a picker whose provider lists no models.
export const typedIdHint = 'This provider lists no models. Type any model id it accepts.'
