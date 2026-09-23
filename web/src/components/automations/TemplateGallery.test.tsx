import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { TemplateGallery } from './TemplateGallery'
import { makeTemplate } from './testFixtures'

vi.mock('../../api/client', () => ({ listAutomationTemplates: vi.fn() }))

import { listAutomationTemplates } from '../../api/client'

function renderGallery() {
  const router = createMemoryRouter(
    [
      { path: '/automations', element: <TemplateGallery /> },
      { path: '/automations/new', element: <div>editor page</div> },
      { path: '/settings/destinations', element: <div>destinations page</div> },
    ],
    { initialEntries: ['/automations'] },
  )
  const result = render(<RouterProvider router={router} />)
  return { router, ...result }
}

beforeEach(() => vi.clearAllMocks())

describe('TemplateGallery', () => {
  it('renders a card per template with its trigger badges', async () => {
    vi.mocked(listAutomationTemplates).mockResolvedValue([makeTemplate(), makeTemplate({ id: 'digest', name: 'Morning digest', icon: 'unknown' })])
    renderGallery()
    expect(await screen.findByText('Review new pull requests')).toBeInTheDocument()
    expect(screen.getByText('Morning digest')).toBeInTheDocument()
    expect(screen.getAllByText('Hourly')).toHaveLength(2)
  })

  it('shows what is missing with a link to the right settings page', async () => {
    vi.mocked(listAutomationTemplates).mockResolvedValue([makeTemplate({ missing: [{ kind: 'destination', value: 'email' }] })])
    const { router } = renderGallery()
    expect(await screen.findByText(/Needs:/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('link', { name: 'email destination' }))
    expect(router.state.location.pathname).toBe('/settings/destinations')
  })

  it('shows no needs line when nothing is missing', async () => {
    vi.mocked(listAutomationTemplates).mockResolvedValue([makeTemplate()])
    renderGallery()
    await screen.findByText('Review new pull requests')
    expect(screen.queryByText(/Needs:/)).toBeNull()
  })

  it('opens the editor with the template in router state', async () => {
    const template = makeTemplate()
    vi.mocked(listAutomationTemplates).mockResolvedValue([template])
    const { router } = renderGallery()
    fireEvent.click(await screen.findByRole('button', { name: /Review new pull requests/ }))
    expect(router.state.location.pathname).toBe('/automations/new')
    expect(router.state.location.state).toEqual({ template })
  })

  it('renders nothing when the endpoint 404s', async () => {
    vi.mocked(listAutomationTemplates).mockRejectedValue(Object.assign(new Error('not found'), { status: 404 }))
    const { container } = renderGallery()
    await waitFor(() => expect(listAutomationTemplates).toHaveBeenCalled())
    expect(container).toBeEmptyDOMElement()
  })

  it('renders nothing for an empty list', async () => {
    vi.mocked(listAutomationTemplates).mockResolvedValue([])
    const { container } = renderGallery()
    await waitFor(() => expect(listAutomationTemplates).toHaveBeenCalled())
    expect(container).toBeEmptyDOMElement()
  })
})
