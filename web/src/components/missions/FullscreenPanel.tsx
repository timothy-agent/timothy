import { FullScreenIcon, Minimize01Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useState } from 'react'
import { Button } from '../ui/button'
import { Dialog, DialogContent } from '../ui/dialog'

// FullscreenToggle renders one icon button that flips `fullscreen`.
// Shared by TimelineSection/ArtifactsSection so both grow into the
// same near-viewport Dialog instead of each hand-rolling an overlay.
export function FullscreenToggle({
  fullscreen,
  onToggle,
}: {
  fullscreen: boolean
  onToggle: () => void
}) {
  return (
    <Button
      variant="ghost"
      size="icon-xs"
      title={fullscreen ? 'Exit fullscreen' : 'Fullscreen'}
      onClick={onToggle}
    >
      <HugeiconsIcon icon={fullscreen ? Minimize01Icon : FullScreenIcon} />
    </Button>
  )
}

// useFullscreenPanel gives a panel a `fullscreen` flag plus the props
// to spread on a Dialog wrapping its content when active. The panel
// renders its own header/body once; the caller just switches height
// classes on `fullscreen` and, when true, wraps the same JSX in the
// returned Dialog.
export function useFullscreenPanel() {
  const [fullscreen, setFullscreen] = useState(false)
  return {
    fullscreen,
    toggle: () => setFullscreen((v) => !v),
    close: () => setFullscreen(false),
  }
}

export function FullscreenDialog({
  open,
  onOpenChange,
  children,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  children: React.ReactNode
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex h-[calc(100vh-4rem)] max-w-[calc(100vw-4rem)] flex-col p-0 sm:max-w-[calc(100vw-4rem)]">
        {children}
      </DialogContent>
    </Dialog>
  )
}
