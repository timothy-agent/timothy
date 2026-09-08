import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { SettingsNav } from './SettingsNav'
import { settingsAreas } from './settingsAreas'

afterEach(cleanup)

function LocationProbe() {
  const location = useLocation()
  return <div data-testid="location">{location.pathname}</div>
}

function renderNav(initialEntry = '/settings/providers') {
  return render(
    <MemoryRouter initialEntries={[initialEntry]}>
      <SettingsNav areas={settingsAreas} current="providers" />
      <Routes>
        <Route path="*" element={<LocationProbe />} />
      </Routes>
    </MemoryRouter>,
  )
}

describe('SettingsNav desktop', () => {
  it('renders a nav named Settings with eight links, current one marked', () => {
    renderNav()
    const nav = screen.getByRole('navigation', { name: 'Settings' })
    const links = screen.getAllByRole('link')
    expect(links).toHaveLength(8)

    const current = links.find((l) => l.getAttribute('aria-current') === 'page')
    expect(current?.textContent).toBe('Providers')

    for (const link of links) {
      if (link !== current) expect(link.getAttribute('aria-current')).toBeNull()
    }
    expect(nav).toBeTruthy()
  })

  it('navigates when another link is clicked', () => {
    renderNav()
    fireEvent.click(screen.getByRole('link', { name: 'Secrets' }))
    expect(screen.getByTestId('location').textContent).toBe('/settings/secrets')
  })
})

describe('SettingsNav mobile', () => {
  const originalWidth = window.innerWidth

  beforeEach(() => {
    window.innerWidth = 500
  })

  afterEach(() => {
    window.innerWidth = originalWidth
  })

  it('shows a combobox labelled Settings area and no Settings nav', () => {
    renderNav()
    expect(screen.getByRole('combobox', { name: 'Settings area' })).toBeTruthy()
    expect(screen.queryByRole('navigation', { name: 'Settings' })).toBeNull()
  })

  it('navigates when another area is selected', async () => {
    Element.prototype.scrollIntoView = () => {}
    renderNav()
    const trigger = screen.getByRole('combobox', { name: 'Settings area' })
    fireEvent.click(trigger)
    fireEvent.click(await screen.findByText('Secrets'))
    expect(screen.getByTestId('location').textContent).toBe('/settings/secrets')
  })
})
