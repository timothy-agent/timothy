import { cleanup, renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdminProvider } from '../api/types'

vi.mock('../api/client', () => ({
  availableModels: vi.fn(),
  catalogModelsForProvider: vi.fn(),
  listAgents: vi.fn(),
  listProviders: vi.fn(),
  listRoutes: vi.fn(),
  searchCatalog: vi.fn(),
}))

import { availableModels, catalogModelsForProvider, listProviders, searchCatalog } from '../api/client'
import { resetProviderModelsCache, useProviderModels } from './AgentPicker'

function provider(over: Partial<AdminProvider>): AdminProvider {
  return {
    id: 'p',
    name: 'P',
    kind: 'api',
    driver: 'openaicompat',
    base_url: '',
    default_model: '',
    credential_ref: '',
    headers: {},
    enabled: true,
    ...over,
  }
}

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
  resetProviderModelsCache()
})

describe('useProviderModels', () => {
  it('reads each enabled api provider\'s unified model list', async () => {
    vi.mocked(listProviders).mockResolvedValue([
      provider({ id: 'p-cgpt', name: 'ChatGPT Subscription', base_url: 'http://cliproxy:8317/v1', default_model: 'gpt-5' }),
      provider({ id: 'p-off', name: 'Off', enabled: false }),
      provider({ id: 'p-cli', name: 'Claude Code', kind: 'cli', driver: 'claude-cli' }),
    ])
    vi.mocked(availableModels).mockResolvedValue([
      { id: 'gpt-5', source: 'live' },
      { id: 'gpt-5-mini', display_name: 'GPT-5 mini', source: 'live' },
    ])

    const { result } = renderHook(() => useProviderModels())

    await waitFor(() => expect(result.current).not.toBeNull())
    expect(result.current).toEqual([
      { provider_name: 'ChatGPT Subscription', model: 'gpt-5', display_name: undefined, value: 'ChatGPT Subscription/gpt-5' },
      { provider_name: 'ChatGPT Subscription', model: 'gpt-5-mini', display_name: 'GPT-5 mini', value: 'ChatGPT Subscription/gpt-5-mini' },
    ])
    expect(availableModels).toHaveBeenCalledTimes(1)
    expect(availableModels).toHaveBeenCalledWith('p-cgpt')
    expect(catalogModelsForProvider).not.toHaveBeenCalled()
    expect(searchCatalog).not.toHaveBeenCalled()
  })

  it('offers the default model alone when the list is empty or fails', async () => {
    vi.mocked(listProviders).mockResolvedValue([
      provider({ id: 'p-empty', name: 'Empty', default_model: 'house-model' }),
      provider({ id: 'p-bad', name: 'Bad', default_model: 'bad-default' }),
      provider({ id: 'p-none', name: 'None' }),
    ])
    vi.mocked(availableModels).mockImplementation(async (id: string) => {
      if (id === 'p-bad') throw new Error('http 401')
      return []
    })

    const { result } = renderHook(() => useProviderModels())

    await waitFor(() => expect(result.current).not.toBeNull())
    expect(result.current?.map((o) => o.value)).toEqual(['Empty/house-model', 'Bad/bad-default'])
  })
})
