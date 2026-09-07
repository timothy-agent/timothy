import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { Table, TableHeader, TableRow, TableHead, TableBody, TableCell, TableCaption } from './table'

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
})
