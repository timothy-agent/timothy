import axe from 'axe-core'
import { cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, describe, expect, it } from 'vitest'
import { TooltipProvider } from '@/components/ui/tooltip'
import { DesignSystem } from './DesignSystem'

afterEach(cleanup)

const sectionTitles = [
  'Typography scale',
  'Colour roles',
  'Status',
  'Spacing',
  'Radius',
  'Elevation',
  'Button',
  'Input',
  'Select',
  'Textarea',
  'Checkbox',
  'Radio',
  'Switch',
  'Badge',
  'Tabs',
  'Combobox',
  'Segmented control',
  'Tooltip and Kbd',
  'Focus',
  'Page header',
  'Form',
  'Panel',
  'Mission cards',
  'Empty state',
  'Dialog',
  'Copy button, JsonBlock, Kbd',
  'Conversation',
  'Agent status line',
  'Approval',
  'Event log',
  'Mission header',
  'Provider cards',
  'Side by side',
]

describe('DesignSystem', () => {
  it('renders the full page, agent status lines and event log, with no axe violations', async () => {
    const { container } = render(
      <MemoryRouter initialEntries={['/design']}>
        <TooltipProvider>
          <DesignSystem />
        </TooltipProvider>
      </MemoryRouter>,
    )

    const h1s = screen.getAllByRole('heading', { level: 1 })
    expect(h1s).toHaveLength(1)
    expect(h1s[0]).toHaveTextContent('Design system')

    for (const title of sectionTitles) {
      expect(screen.getByRole('heading', { level: 2, name: title })).toBeInTheDocument()
    }

    const statusLines = screen.getAllByRole('status')
    expect(statusLines.length).toBeGreaterThanOrEqual(4)

    expect(screen.getAllByRole('log').length).toBeGreaterThanOrEqual(1)

    const results = await axe.run(container, {
      rules: { 'color-contrast': { enabled: false }, region: { enabled: false } },
    })
    expect(results.violations).toEqual([])
  }, 10000)
})
