import axe from 'axe-core'
import { FileText } from 'lucide-react'
import { cleanup, render } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { TooltipProvider } from '@/components/ui/tooltip'
import { CopyButton } from './copy-button'
import { IconButton } from './icon-button'
import { JsonBlock } from './json-block'
import { Kbd, KbdGroup } from './kbd'
import { Spinner } from './spinner'
import { StatusBadge } from './status-badge'
import { StatusDot } from './status-dot'
import { TraceGroup, TraceRow } from './trace-group'

afterEach(cleanup)

describe('timothy components a11y', () => {
  it('has no axe violations', async () => {
    const { container } = render(
      <TooltipProvider>
        <main>
          <StatusBadge status="working" />
          <StatusDot status="success" label="Done" />
          <Spinner label="Loading" />
          <KbdGroup>
            <Kbd>Ctrl</Kbd>
            <Kbd>S</Kbd>
          </KbdGroup>
          <IconButton label="Read file" icon={FileText} />
          <CopyButton value="hello" />
          <JsonBlock value={{ a: 1 }} label="payload" />
          <TraceGroup summary="2 tool calls" duration="1.0s">
            <TraceRow status="success" icon={FileText} action="Read" target="src/loop.go" duration="12ms" />
          </TraceGroup>
        </main>
      </TooltipProvider>,
    )

    const results = await axe.run(container, { rules: { 'color-contrast': { enabled: false } } })
    expect(results.violations).toEqual([])
  })
})
