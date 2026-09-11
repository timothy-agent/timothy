import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { FeaturesTab } from './FeaturesTab'
import type { KbCollection } from '../../api/types'

// FeaturesTab now renders PageHeader's breadcrumb links, which need a
// Router context, so every render is wrapped in MemoryRouter.
function renderTab() {
  return render(
    <MemoryRouter>
      <FeaturesTab />
    </MemoryRouter>,
  )
}

vi.mock('../../api/client', () => ({
  getSettings: vi.fn(),
  listKbCollections: vi.fn(),
  listRoutes: vi.fn(),
  patchSettings: vi.fn(),
  patchSettingValues: vi.fn(),
}))
vi.mock('sonner', () => ({ toast: { error: vi.fn(), success: vi.fn() } }))

import { getSettings, listKbCollections, listRoutes, patchSettings, patchSettingValues } from '../../api/client'
import { toast } from 'sonner'

afterEach(cleanup)
beforeEach(() => {
  Element.prototype.scrollIntoView = vi.fn()
  vi.clearAllMocks()
  localStorage.clear()
  vi.mocked(listRoutes).mockResolvedValue([])
  vi.mocked(listKbCollections).mockResolvedValue([])
  vi.mocked(patchSettingValues).mockResolvedValue(undefined)
})

function kbCollection(name: string): KbCollection {
  return {
    id: name,
    name,
    description: '',
    doc_count: 0,
    chunk_count: 0,
    failed_count: 0,
    retrieval_weight: 1,
    created_at: '2026-09-11T00:00:00Z',
    updated_at: '2026-09-11T00:00:00Z',
  }
}

describe('FeaturesTab review token ceiling', () => {
  it('shows the stored ceiling beside the run budget and saves an edit', async () => {
    vi.mocked(getSettings).mockResolvedValue({
      settings: {},
      values: { executor_run_budget_minutes: '90', mission_review_token_ceiling: '250000' },
    })
    renderTab()
    const input = (await screen.findByRole('spinbutton', { name: 'Review token ceiling' })) as HTMLInputElement
    expect(input.value).toBe('250000')
    expect(screen.getByRole('spinbutton', { name: 'Harness run budget minutes' })).toBeTruthy()

    const region = screen.getByRole('region', { name: 'Review token ceiling' })
    const saveButton = within(region).getByRole('button', { name: 'Save' })
    expect(saveButton).toBeDisabled()

    fireEvent.change(input, { target: { value: '0' } })
    expect(saveButton).toBeEnabled()
    fireEvent.click(saveButton)
    await waitFor(() => expect(patchSettingValues).toHaveBeenCalledWith({ mission_review_token_ceiling: '0' }))
  })

  it('shows the default as a placeholder when unset', async () => {
    vi.mocked(getSettings).mockResolvedValue({ settings: {}, values: {} })
    renderTab()
    const input = (await screen.findByRole('spinbutton', { name: 'Review token ceiling' })) as HTMLInputElement
    expect(input.value).toBe('')
    expect(input.placeholder).toBe('1500000')
  })

  it('failed Save keeps the value and shows an alert with Retry', async () => {
    vi.mocked(getSettings).mockResolvedValue({
      settings: {},
      values: { mission_review_token_ceiling: '250000' },
    })
    vi.mocked(patchSettingValues).mockRejectedValueOnce(new Error('network down'))
    renderTab()

    const input = (await screen.findByRole('spinbutton', { name: 'Review token ceiling' })) as HTMLInputElement
    fireEvent.change(input, { target: { value: '0' } })
    const region = screen.getByRole('region', { name: 'Review token ceiling' })
    fireEvent.click(within(region).getByRole('button', { name: 'Save' }))

    const alert = await within(region).findByRole('alert')
    expect(alert).toHaveTextContent('network down')
    expect(input.value).toBe('0')

    vi.mocked(patchSettingValues).mockResolvedValueOnce(undefined)
    fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(patchSettingValues).toHaveBeenCalledWith({ mission_review_token_ceiling: '0' }))
    expect(within(region).queryByRole('alert')).toBeNull()
  })
})

describe('FeaturesTab flag flip', () => {
  it('flips a flag optimistically and commits the PATCH', async () => {
    vi.mocked(getSettings).mockResolvedValue({ settings: { tools_enabled: true }, values: {} })
    vi.mocked(patchSettings).mockResolvedValue(undefined)
    render(
      <MemoryRouter>
        <FeaturesTab />
      </MemoryRouter>,
    )

    const toggle = await screen.findByRole('switch', { name: 'Tool execution' })
    expect(toggle).toHaveAttribute('data-state', 'checked')
    fireEvent.click(toggle)

    expect(toggle).toHaveAttribute('data-state', 'unchecked')
    await waitFor(() => expect(patchSettings).toHaveBeenCalledWith({ tools_enabled: false }))
  })

  it('reverts and toasts when the flag PATCH fails', async () => {
    vi.mocked(getSettings).mockResolvedValue({ settings: { tools_enabled: true }, values: {} })
    vi.mocked(patchSettings).mockRejectedValueOnce(new Error('server unavailable'))
    render(
      <MemoryRouter>
        <FeaturesTab />
      </MemoryRouter>,
    )

    const toggle = await screen.findByRole('switch', { name: 'Tool execution' })
    fireEvent.click(toggle)

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Could not save', { description: 'server unavailable' }),
    )
    // flip's catch refetches settings on failure; a successful refetch
    // clears the error state right after setting it, so the switch
    // reverting to its server value is the only lasting effect here.
    await waitFor(() => expect(toggle).toHaveAttribute('data-state', 'checked'))
  })
})

describe('FeaturesTab notification sound', () => {
  it('toggles the localStorage-backed sound preference with no PATCH', async () => {
    vi.mocked(getSettings).mockResolvedValue({ settings: {}, values: {} })
    renderTab()

    const toggle = await screen.findByRole('switch', { name: 'Notification sound' })
    expect(toggle).toHaveAttribute('data-state', 'checked')
    fireEvent.click(toggle)

    expect(toggle).toHaveAttribute('data-state', 'unchecked')
    expect(localStorage.getItem('timothy.notificationSound')).toBe('off')
    expect(patchSettingValues).not.toHaveBeenCalled()
  })
})

describe('FeaturesTab plain value cards', () => {
  it('saves an edited harness run budget, trimmed', async () => {
    vi.mocked(getSettings).mockResolvedValue({ settings: {}, values: {} })
    renderTab()

    const input = await screen.findByRole('spinbutton', { name: 'Harness run budget minutes' })
    fireEvent.change(input, { target: { value: '90' } })
    const region = screen.getByRole('region', { name: 'Harness run budget' })
    fireEvent.click(within(region).getByRole('button', { name: 'Save' }))

    await waitFor(() =>
      expect(patchSettingValues).toHaveBeenCalledWith({ executor_run_budget_minutes: '90' }),
    )
  })

  it('saves an edited default branch pattern, trimmed', async () => {
    vi.mocked(getSettings).mockResolvedValue({ settings: {}, values: {} })
    renderTab()

    const input = await screen.findByRole('textbox', { name: 'Default branch pattern' })
    fireEvent.change(input, { target: { value: ' {type}/{slug} ' } })
    const region = screen.getByRole('region', { name: 'Default branch pattern' })
    fireEvent.click(within(region).getByRole('button', { name: 'Save' }))

    await waitFor(() =>
      expect(patchSettingValues).toHaveBeenCalledWith({ git_branch_pattern: '{type}/{slug}' }),
    )
  })

  it('saves an edited writing style, trimmed', async () => {
    vi.mocked(getSettings).mockResolvedValue({ settings: {}, values: {} })
    renderTab()

    const textarea = await screen.findByRole('textbox', { name: 'Writing style' })
    expect(textarea).toHaveAttribute('maxlength', '4000')
    fireEvent.change(textarea, { target: { value: '  Short sentences. British spelling.  ' } })
    const region = screen.getByRole('region', { name: 'Writing style' })
    fireEvent.click(within(region).getByRole('button', { name: 'Save' }))

    await waitFor(() =>
      expect(patchSettingValues).toHaveBeenCalledWith({ writing_style: 'Short sentences. British spelling.' }),
    )
  })
})

describe('FeaturesTab writing samples collection', () => {
  it('picks a collection from the loaded list and saves its name', async () => {
    vi.mocked(getSettings).mockResolvedValue({ settings: {}, values: {} })
    vi.mocked(listKbCollections).mockResolvedValue([kbCollection('my-writing')])
    renderTab()

    fireEvent.click(await screen.findByRole('combobox', { name: 'Writing samples collection' }))
    fireEvent.click(await screen.findByRole('option', { name: 'my-writing' }))
    const region = screen.getByRole('region', { name: 'Writing samples' })
    fireEvent.click(within(region).getByRole('button', { name: 'Save' }))

    await waitFor(() =>
      expect(patchSettingValues).toHaveBeenCalledWith({ writing_samples_collection: 'my-writing' }),
    )
  })

  it('falls back to a plain text input when the collection list fails to load', async () => {
    vi.mocked(getSettings).mockResolvedValue({
      settings: {},
      values: { writing_samples_collection: 'my-writing' },
    })
    vi.mocked(listKbCollections).mockRejectedValue(new Error('down'))
    renderTab()

    const input = await screen.findByRole('textbox', { name: 'Writing samples collection' })
    expect((input as HTMLInputElement).value).toBe('my-writing')
  })

  it('keeps a stored name missing from the list selectable, marked missing', async () => {
    vi.mocked(getSettings).mockResolvedValue({
      settings: {},
      values: { writing_samples_collection: 'renamed-away' },
    })
    vi.mocked(listKbCollections).mockResolvedValue([kbCollection('my-writing')])
    renderTab()

    const trigger = await screen.findByRole('combobox', { name: 'Writing samples collection' })
    await waitFor(() => expect(trigger).toHaveTextContent('renamed-away (missing)'))
  })

  it('shows a retryable alert when saving the collection fails', async () => {
    vi.mocked(getSettings).mockResolvedValue({ settings: {}, values: {} })
    vi.mocked(listKbCollections).mockResolvedValue([kbCollection('my-writing')])
    vi.mocked(patchSettingValues).mockRejectedValueOnce(new Error('network down'))
    renderTab()

    fireEvent.click(await screen.findByRole('combobox', { name: 'Writing samples collection' }))
    fireEvent.click(await screen.findByRole('option', { name: 'my-writing' }))
    const region = screen.getByRole('region', { name: 'Writing samples' })
    fireEvent.click(within(region).getByRole('button', { name: 'Save' }))

    const alert = await within(region).findByRole('alert')
    expect(alert).toHaveTextContent('network down')
  })
})

describe('FeaturesTab sensitive tool route', () => {
  it('picks a route from the loaded list and saves it', async () => {
    vi.mocked(getSettings).mockResolvedValue({ settings: {}, values: {} })
    vi.mocked(listRoutes).mockResolvedValue([
      { name: 'local-only', chain: [], strategy: 'ordered', enabled: true },
    ])
    renderTab()

    fireEvent.click(await screen.findByRole('combobox', { name: 'Sensitive tool route' }))
    fireEvent.click(await screen.findByRole('option', { name: 'local-only' }))
    const region = screen.getByRole('region', { name: 'Sensitive tool route' })
    fireEvent.click(within(region).getByRole('button', { name: 'Save' }))

    await waitFor(() =>
      expect(patchSettingValues).toHaveBeenCalledWith({ sensitive_tool_route: 'local-only' }),
    )
  })

  it('falls back to a plain text input when the route list fails to load', async () => {
    vi.mocked(getSettings).mockResolvedValue({ settings: {}, values: { sensitive_tool_route: 'local-only' } })
    vi.mocked(listRoutes).mockRejectedValue(new Error('down'))
    renderTab()

    const input = await screen.findByRole('textbox', { name: 'Sensitive tool route' })
    expect((input as HTMLInputElement).value).toBe('local-only')
  })

  it('shows a retryable alert when saving the sensitive route fails', async () => {
    vi.mocked(getSettings).mockResolvedValue({ settings: {}, values: {} })
    vi.mocked(listRoutes).mockResolvedValue([
      { name: 'local-only', chain: [], strategy: 'ordered', enabled: true },
    ])
    vi.mocked(patchSettingValues).mockRejectedValueOnce(new Error('network down'))
    renderTab()

    fireEvent.click(await screen.findByRole('combobox', { name: 'Sensitive tool route' }))
    fireEvent.click(await screen.findByRole('option', { name: 'local-only' }))
    const region = screen.getByRole('region', { name: 'Sensitive tool route' })
    fireEvent.click(within(region).getByRole('button', { name: 'Save' }))

    const alert = await within(region).findByRole('alert')
    expect(alert).toHaveTextContent('network down')
  })
})

describe('FeaturesTab load error', () => {
  it('shows an inline alert when loading settings fails', async () => {
    vi.mocked(getSettings).mockRejectedValue(new Error('network down'))
    renderTab()

    expect(await screen.findByRole('alert')).toHaveTextContent('network down')
  })
})

describe('FeaturesTab timezone fallback list', () => {
  it('falls back to the built-in timezone list when Intl.supportedValuesOf throws', async () => {
    const original = Intl.supportedValuesOf
    Intl.supportedValuesOf = () => {
      throw new Error('unsupported')
    }
    try {
      vi.mocked(getSettings).mockResolvedValue({ settings: {}, values: {} })
      renderTab()

      fireEvent.click(await screen.findByRole('combobox', { name: 'Timezone' }))
      expect(await screen.findByText('Europe/Amsterdam')).toBeTruthy()
    } finally {
      Intl.supportedValuesOf = original
    }
  })
})

describe('FeaturesTab timezone', () => {
  it('shows the Off option and picking it saves an empty timezone', async () => {
    vi.mocked(getSettings).mockResolvedValue({ settings: {}, values: { timezone: 'Europe/Amsterdam' } })
    renderTab()

    fireEvent.click(await screen.findByRole('combobox', { name: 'Timezone' }))
    fireEvent.click(await screen.findByText('UTC (default)'))

    await waitFor(() => expect(patchSettingValues).toHaveBeenCalledWith({ timezone: '' }))
  })

  it('toasts when saving the timezone fails', async () => {
    vi.mocked(getSettings).mockResolvedValue({ settings: {}, values: {} })
    vi.mocked(patchSettingValues).mockRejectedValueOnce(new Error('server unavailable'))
    renderTab()

    fireEvent.click(await screen.findByRole('combobox', { name: 'Timezone' }))
    fireEvent.click(await screen.findByText('Europe/Amsterdam'))

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Could not save timezone', { description: 'server unavailable' }),
    )
  })
})
