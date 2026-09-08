import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import {
  Table,
  TableHeader,
  TableRow,
  TableHead,
  TableBody,
  TableCell,
  TableCaption,
  TableFooter,
} from './table'

afterEach(cleanup)

describe('Table', () => {
  it('renders a caption and applies tabular-nums to numeric cells', () => {
    render(
      <Table>
        <TableCaption>Recent costs</TableCaption>
        <TableHeader>
          <TableRow>
            <TableHead>Model</TableHead>
            <TableHead numeric>Cost</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          <TableRow>
            <TableCell>glm-4.7</TableCell>
            <TableCell numeric>$0.04</TableCell>
          </TableRow>
        </TableBody>
      </Table>,
    )
    expect(screen.getByText('Recent costs')).toBeInTheDocument()
    expect(screen.getByText('$0.04').className).toContain('tabular-nums')
    expect(screen.getByText('Cost').className).toContain('tabular-nums')
  })

  it('renders a footer with the muted background classes', () => {
    render(
      <Table>
        <TableFooter>
          <TableRow>
            <TableCell>Total</TableCell>
          </TableRow>
        </TableFooter>
      </Table>,
    )
    const footer = screen.getByText('Total').closest('tfoot')
    expect(footer).toHaveAttribute('data-slot', 'table-footer')
    expect(footer?.className).toContain('bg-muted/40')
  })
})
