import axe from 'axe-core'
import { cleanup, render } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { TooltipProvider } from '@/components/ui/tooltip'
import { AgentStatusLine } from './AgentStatusLine'
import { ToolCallGroup } from './ToolCallCard'
import { ApprovalCard } from './ApprovalCard'
import type { ToolRun } from '@/lib/chat'
import type { PermissionRequestEvent } from '@/api/types'

afterEach(cleanup)

const runs: ToolRun[] = [
  { id: '1', name: 'search_web', status: 'ok', args: JSON.stringify({ query: 'pgvector rrf' }), durationMs: 640 },
  { id: '2', name: 'read_file', status: 'ok', args: JSON.stringify({ path: 'src/loop.go' }), durationMs: 12 },
  { id: '3', name: 'shell', status: 'error', digest: 'exit 1' },
]

const request: PermissionRequestEvent = {
  id: 'perm-1',
  call_id: 'call-1',
  tool: 'shell',
  args: JSON.stringify({ command: 'go test ./...' }),
  danger_level: 'destructive',
  rationale: 'Confirms the change did not regress tests.',
}

describe('chat components a11y', () => {
  it('has no axe violations', async () => {
    const { container } = render(
      <TooltipProvider>
        <main>
          <AgentStatusLine phase={{ kind: 'thinking' }} />
          <AgentStatusLine phase={{ kind: 'waiting', what: 'approval', onFocusRequest: () => {} }} />
          <AgentStatusLine phase={{ kind: 'error', message: 'boom', onRetry: () => {} }} />
          <AgentStatusLine phase={{ kind: 'done', durationMs: 4200, toolCount: 3 }} />
          <ToolCallGroup runs={runs} defaultOpen />
          <ApprovalCard request={request} onDecision={() => {}} requestedAt={new Date()} />
        </main>
      </TooltipProvider>,
    )

    const results = await axe.run(container, { rules: { 'color-contrast': { enabled: false } } })
    expect(results.violations).toEqual([])
  })
})
