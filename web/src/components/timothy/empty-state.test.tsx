import { Inbox } from 'lucide-react'
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { EmptyState } from './empty-state'

describe('EmptyState', () => {
  it('renders the title, description and action', () => {
    render(
      <EmptyState
        icon={Inbox}
        title="No missions yet"
        description="Missions you start will show up here."
        action={<button>New mission</button>}
      />,
    )
    expect(screen.getByText('No missions yet')).toBeInTheDocument()
    expect(screen.getByText('Missions you start will show up here.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'New mission' })).toBeInTheDocument()
  })
})
