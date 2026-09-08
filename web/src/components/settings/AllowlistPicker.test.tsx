import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdminSkill, AdminTool, KbCollection } from '../../api/types'

vi.mock('../../api/client', () => ({
  listSkills: vi.fn(),
  listTools: vi.fn(),
  listKbCollections: vi.fn(),
}))

const skills: AdminSkill[] = [
  { name: 'coding', description: 'Write code.' },
  { name: 'research-brief', description: 'Research and summarize.' },
]
const tools: AdminTool[] = [{ name: 'search_web', description: 'Search the web.' }]
const collections: KbCollection[] = [
  {
    id: 'c1',
    name: 'product-docs',
    description: 'Product documentation.',
    doc_count: 1,
    chunk_count: 5,
    failed_count: 0,
    retrieval_weight: 1.0,
    created_at: '2026-08-01T00:00:00Z',
    updated_at: '2026-08-01T00:00:00Z',
  },
]

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
  // jsdom lacks scrollIntoView; cmdk calls it when an item mounts.
  Element.prototype.scrollIntoView = vi.fn()
})

// Every AllowlistPicker instance shares useCachedList's module-level
// cache; each test resets modules and re-imports fresh so one test's
// fetch cannot leak into the next.
async function freshPicker() {
  vi.resetModules()
  const client = await import('../../api/client')
  const { AllowlistPicker } = await import('./AllowlistPicker')
  return { AllowlistPicker, ...client }
}

describe('AllowlistPicker: skills', () => {
  it('picks a skill from the combobox and marks it aria-multiselectable', async () => {
    const { AllowlistPicker, listSkills } = await freshPicker()
    vi.mocked(listSkills).mockResolvedValue(skills)
    const onChange = vi.fn()
    render(
      <AllowlistPicker
        label="Skills allowlist"
        value={[]}
        onChange={onChange}
        load={async () => (await listSkills()).map((s) => ({ id: s.name, label: s.name }))}
        cacheKey="skills"
        emptyText="No skill matches."
        freeTextPlaceholder="research-brief, coding"
      />,
    )

    const trigger = await screen.findByRole('combobox', { name: 'Skills allowlist' })
    fireEvent.click(trigger)
    const listbox = await screen.findByRole('listbox')
    expect(listbox).toHaveAttribute('aria-multiselectable', 'true')
    fireEvent.click(within(listbox).getByText('coding'))
    expect(onChange).toHaveBeenCalledWith(['coding'])
  })

  it('shows each option\'s description in the open list', async () => {
    const { AllowlistPicker, listSkills } = await freshPicker()
    vi.mocked(listSkills).mockResolvedValue(skills)
    render(
      <AllowlistPicker
        label="Skills allowlist"
        value={[]}
        onChange={vi.fn()}
        load={async () =>
          (await listSkills()).map((s) => ({ id: s.name, label: s.name, description: s.description }))
        }
        cacheKey="skills-description"
        emptyText="No skill matches."
        freeTextPlaceholder="research-brief, coding"
      />,
    )

    const trigger = await screen.findByRole('combobox', { name: 'Skills allowlist' })
    fireEvent.click(trigger)
    const listbox = await screen.findByRole('listbox')
    expect(within(listbox).getByText('Write code.')).toBeInTheDocument()
  })

  it('toggles a highlighted skill with Enter', async () => {
    const { AllowlistPicker, listSkills } = await freshPicker()
    vi.mocked(listSkills).mockResolvedValue(skills)
    const onChange = vi.fn()
    render(
      <AllowlistPicker
        label="Skills allowlist"
        value={[]}
        onChange={onChange}
        load={async () => (await listSkills()).map((s) => ({ id: s.name, label: s.name }))}
        cacheKey="skills-kb"
        emptyText="No skill matches."
        freeTextPlaceholder="research-brief, coding"
      />,
    )

    const trigger = await screen.findByRole('combobox', { name: 'Skills allowlist' })
    fireEvent.click(trigger)
    screen.getByRole('listbox')
    fireEvent.keyDown(screen.getByPlaceholderText('Search...'), { key: 'Enter' })
    await waitFor(() => expect(onChange).toHaveBeenCalledWith(['coding']))
  })

  it('fetches skills once across two mounts (module-level cache)', async () => {
    const { AllowlistPicker, listSkills } = await freshPicker()
    vi.mocked(listSkills).mockResolvedValue(skills)
    const load = async () => (await listSkills()).map((s) => ({ id: s.name, label: s.name }))
    const props = {
      label: 'Skills allowlist',
      value: [],
      onChange: vi.fn(),
      load,
      cacheKey: 'skills-cache',
      emptyText: 'No skill matches.',
      freeTextPlaceholder: 'research-brief, coding',
    }

    const { unmount } = render(<AllowlistPicker {...props} />)
    await screen.findByLabelText('Skills allowlist')
    unmount()

    render(<AllowlistPicker {...props} />)
    await screen.findByLabelText('Skills allowlist')

    expect(listSkills).toHaveBeenCalledTimes(1)
  })

  it('falls back to free text when the skill list fetch fails', async () => {
    const { AllowlistPicker, listSkills } = await freshPicker()
    vi.mocked(listSkills).mockRejectedValue(new Error('boom'))
    const onChange = vi.fn()
    render(
      <AllowlistPicker
        label="Skills allowlist"
        value={[]}
        onChange={onChange}
        load={async () => (await listSkills()).map((s) => ({ id: s.name, label: s.name }))}
        cacheKey="skills-failed"
        emptyText="No skill matches."
        freeTextPlaceholder="research-brief, coding"
      />,
    )

    const input = await screen.findByPlaceholderText('research-brief, coding')
    expect(input).toHaveAccessibleName('Skills allowlist')
    fireEvent.change(input, { target: { value: 'coding, research' } })
    expect(onChange).toHaveBeenCalledWith(['coding', 'research'])
  })
})

describe('AllowlistPicker: tools', () => {
  it('removes a selected tool via its chip', async () => {
    const { AllowlistPicker, listTools } = await freshPicker()
    vi.mocked(listTools).mockResolvedValue(tools)
    const onChange = vi.fn()
    render(
      <AllowlistPicker
        label="Tools allowlist"
        value={['search_web']}
        onChange={onChange}
        load={async () => (await listTools()).map((t) => ({ id: t.name, label: t.name }))}
        cacheKey="tools"
        emptyText="No tool matches."
        freeTextPlaceholder="search_web, fetch_url, shell"
      />,
    )

    fireEvent.click(await screen.findByLabelText('Remove search_web'))
    expect(onChange).toHaveBeenCalledWith([])
  })

  it('falls back to free text when the tool list is empty', async () => {
    const { AllowlistPicker, listTools } = await freshPicker()
    vi.mocked(listTools).mockResolvedValue([])
    const onChange = vi.fn()
    render(
      <AllowlistPicker
        label="Tools allowlist"
        value={[]}
        onChange={onChange}
        load={async () => (await listTools()).map((t) => ({ id: t.name, label: t.name }))}
        cacheKey="tools-empty"
        emptyText="No tool matches."
        freeTextPlaceholder="search_web, fetch_url, shell"
      />,
    )

    const input = await screen.findByPlaceholderText('search_web, fetch_url, shell')
    fireEvent.change(input, { target: { value: 'a, b' } })
    expect(onChange).toHaveBeenCalledWith(['a', 'b'])
  })
})

describe('AllowlistPicker: knowledge', () => {
  it('shows the placeholder when nothing is selected', async () => {
    const { AllowlistPicker, listKbCollections } = await freshPicker()
    vi.mocked(listKbCollections).mockResolvedValue(collections)
    render(
      <AllowlistPicker
        label="Knowledge allowlist"
        value={[]}
        onChange={vi.fn()}
        load={async () => (await listKbCollections()).map((c) => ({ id: c.name, label: c.name }))}
        cacheKey="knowledge"
        emptyText="No collection matches."
        freeTextPlaceholder="product-docs, runbooks"
      />,
    )
    expect(await screen.findByText('Select...')).toBeTruthy()
  })

  it('associates the Field label with the combobox trigger via id', async () => {
    const { AllowlistPicker, listKbCollections } = await freshPicker()
    vi.mocked(listKbCollections).mockResolvedValue(collections)
    render(
      <AllowlistPicker
        label="Knowledge allowlist"
        value={[]}
        onChange={vi.fn()}
        load={async () => (await listKbCollections()).map((c) => ({ id: c.name, label: c.name }))}
        cacheKey="knowledge-label"
        emptyText="No collection matches."
        freeTextPlaceholder="product-docs, runbooks"
      />,
    )

    const trigger = await screen.findByRole('combobox')
    expect(trigger).toHaveAccessibleName('Knowledge allowlist')
    expect(screen.getByLabelText('Knowledge allowlist')).toBe(trigger)
  })

  it('selects a collection from the popover', async () => {
    const { AllowlistPicker, listKbCollections } = await freshPicker()
    vi.mocked(listKbCollections).mockResolvedValue(collections)
    const onChange = vi.fn()
    render(
      <AllowlistPicker
        label="Knowledge allowlist"
        value={[]}
        onChange={onChange}
        load={async () => (await listKbCollections()).map((c) => ({ id: c.name, label: c.name }))}
        cacheKey="knowledge-select"
        emptyText="No collection matches."
        freeTextPlaceholder="product-docs, runbooks"
      />,
    )

    fireEvent.click(await screen.findByRole('combobox', { name: 'Knowledge allowlist' }))
    fireEvent.click(await screen.findByText('product-docs'))
    expect(onChange).toHaveBeenCalledWith(['product-docs'])
  })

  it('removes a selected collection via its chip', async () => {
    const { AllowlistPicker, listKbCollections } = await freshPicker()
    vi.mocked(listKbCollections).mockResolvedValue(collections)
    const onChange = vi.fn()
    render(
      <AllowlistPicker
        label="Knowledge allowlist"
        value={['product-docs']}
        onChange={onChange}
        load={async () => (await listKbCollections()).map((c) => ({ id: c.name, label: c.name }))}
        cacheKey="knowledge-remove"
        emptyText="No collection matches."
        freeTextPlaceholder="product-docs, runbooks"
      />,
    )

    fireEvent.click(await screen.findByLabelText('Remove product-docs'))
    expect(onChange).toHaveBeenCalledWith([])
  })

  it('falls back to free text when the collection list is empty', async () => {
    const { AllowlistPicker, listKbCollections } = await freshPicker()
    vi.mocked(listKbCollections).mockResolvedValue([])
    const onChange = vi.fn()
    render(
      <AllowlistPicker
        label="Knowledge allowlist"
        value={[]}
        onChange={onChange}
        load={async () => (await listKbCollections()).map((c) => ({ id: c.name, label: c.name }))}
        cacheKey="knowledge-empty"
        emptyText="No collection matches."
        freeTextPlaceholder="product-docs, runbooks"
      />,
    )

    const input = await screen.findByPlaceholderText('product-docs, runbooks')
    fireEvent.change(input, { target: { value: 'a, b' } })
    expect(onChange).toHaveBeenCalledWith(['a', 'b'])
  })
})
