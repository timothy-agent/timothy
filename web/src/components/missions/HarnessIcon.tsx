import { BrandMark } from '../BrandMark'
import { ClaudeCodeIcon } from '../icons/ClaudeCodeIcon'
import { CursorIcon } from '../icons/CursorIcon'
import { OpenAIIcon } from '../icons/OpenAIIcon'
import { OpenCodeIcon } from '../icons/OpenCodeIcon'
import { PiIcon } from '../icons/PiIcon'

// harnessDisplayNames maps a registered harness id to the label shown
// wherever a mission's harness is named (D-051).
const harnessDisplayNames: Record<string, string> = {
  'claude-cli': 'Claude Code',
  pi: 'pi',
  'codex-cli': 'Codex CLI',
  opencode: 'OpenCode',
  'cursor-cli': 'Cursor CLI',
}

export function harnessLabel(harness?: string): string {
  if (!harness) return 'Native'
  return harnessDisplayNames[harness] ?? harness
}

// HarnessIcon picks the mark for a harness id, falling back to the
// Timothy brand mark for a native (undelegated) mission.
export function HarnessIcon({ harness, className }: { harness?: string; className?: string }) {
  if (harness === 'pi') return <PiIcon className={className} />
  if (harness === 'codex-cli') return <OpenAIIcon className={className} />
  if (harness === 'opencode') return <OpenCodeIcon className={className} />
  if (harness === 'claude-cli') return <ClaudeCodeIcon className={className} />
  if (harness === 'cursor-cli') return <CursorIcon className={className} />
  return <BrandMark className={className ?? 'size-3.5 shrink-0 rounded-[3px]'} />
}
