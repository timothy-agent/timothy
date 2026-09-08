import { cleanup, render, screen, fireEvent } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { ToggleGroup, ToggleGroupItem } from './toggle-group'

afterEach(cleanup)

describe('ToggleGroup', () => {
  it('sets data-state=on for the selected item', () => {
    render(
      <ToggleGroup type="single" defaultValue="list">
        <ToggleGroupItem value="list">List</ToggleGroupItem>
        <ToggleGroupItem value="grid">Grid</ToggleGroupItem>
      </ToggleGroup>,
    )
    const list = screen.getByText('List')
    const grid = screen.getByText('Grid')
    expect(list).toHaveAttribute('data-state', 'on')
    expect(grid).toHaveAttribute('data-state', 'off')
    fireEvent.click(grid)
    expect(grid).toHaveAttribute('data-state', 'on')
    expect(list).toHaveAttribute('data-state', 'off')
  })

  it('sizes items sm when the group size is sm', () => {
    render(
      <ToggleGroup type="single" size="sm" defaultValue="list">
        <ToggleGroupItem value="list">List</ToggleGroupItem>
      </ToggleGroup>,
    )
    expect(screen.getByText('List').className).toContain('h-7 px-2.5')
  })
})
