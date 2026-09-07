import { cleanup, render, screen, fireEvent, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { Tabs, TabsList, TabsTrigger, TabsContent } from './tabs'

afterEach(cleanup)

describe('Tabs', () => {
  it('renders tablist/tab roles and moves focus with ArrowRight', async () => {
    render(
      <Tabs defaultValue="one">
        <TabsList>
          <TabsTrigger value="one">One</TabsTrigger>
          <TabsTrigger value="two">Two</TabsTrigger>
        </TabsList>
        <TabsContent value="one">First</TabsContent>
        <TabsContent value="two">Second</TabsContent>
      </Tabs>,
    )
    expect(screen.getByRole('tablist')).toBeInTheDocument()
    const [tabOne, tabTwo] = screen.getAllByRole('tab')
    expect(tabOne).toHaveAttribute('aria-selected', 'true')
    tabOne.focus()
    fireEvent.keyDown(tabOne, { key: 'ArrowRight' })
    await waitFor(() => expect(tabTwo).toHaveFocus())
  })
})
