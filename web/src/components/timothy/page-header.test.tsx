import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { describe, expect, it } from 'vitest'
import { Breadcrumbs, Eyebrow, PageHeader, SectionHeader } from './page-header'

describe('PageHeader', () => {
  it('renders exactly one h1', () => {
    render(<PageHeader title="Missions" />)
    expect(screen.getAllByRole('heading', { level: 1 })).toHaveLength(1)
    expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent('Missions')
  })

  it('renders description and actions', () => {
    render(<PageHeader title="Missions" description="All your missions" actions={<button>New</button>} />)
    expect(screen.getByText('All your missions')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'New' })).toBeInTheDocument()
  })
})

describe('Breadcrumbs', () => {
  it('marks the last crumb as the current page and links the rest', () => {
    render(
      <MemoryRouter>
        <Breadcrumbs
          items={[
            { label: 'Settings', href: '/settings' },
            { label: 'Providers', href: '/settings/providers' },
            { label: 'OpenAI' },
          ]}
        />
      </MemoryRouter>,
    )
    const last = screen.getByText('OpenAI')
    expect(last).toHaveAttribute('aria-current', 'page')
    expect(screen.getByRole('link', { name: 'Settings' })).toHaveAttribute('href', '/settings')
  })

  it('renders a non-last crumb with no href as plain text, not a link', () => {
    render(
      <MemoryRouter>
        <Breadcrumbs items={[{ label: 'Settings' }, { label: 'Current' }]} />
      </MemoryRouter>,
    )
    expect(screen.queryByRole('link', { name: 'Settings' })).not.toBeInTheDocument()
    expect(screen.getByText('Settings')).not.toHaveAttribute('aria-current')
  })
})

describe('SectionHeader', () => {
  it('renders an h2 by default', () => {
    render(<SectionHeader title="Goal" />)
    expect(screen.getByRole('heading', { level: 2 })).toHaveTextContent('Goal')
  })

  it('renders actions when given', () => {
    render(<SectionHeader title="Goal" actions={<button>Edit</button>} />)
    expect(screen.getByRole('button', { name: 'Edit' })).toBeInTheDocument()
  })
})

describe('Eyebrow', () => {
  it('renders uppercase label text', () => {
    render(<Eyebrow>Recent</Eyebrow>)
    expect(screen.getByText('Recent')).toHaveClass('uppercase')
  })
})
