import { ArrowShrink01Icon, FullScreenIcon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useState } from 'react'
import { Button } from '../ui/button'
import { Dialog, DialogContent } from '../ui/dialog'
import { Tooltip, TooltipContent, TooltipTrigger } from '../ui/tooltip'

// FullscreenToggle renders one icon button that flips `fullscreen`.
// Shared by TimelineSection/ArtifactsSection so both grow into the
// same near-viewport Dialog instead of each hand-rolling an overlay.
// Uses ArrowShrink01Icon (not Minimize01Icon, a hand-gesture glyph that
// reads as garbled at this size) so the exit state stays a clean
// corner-arrows mark, matching FullScreenIcon's style.
export function FullscreenToggle({
  fullscreen,
  onToggle,
}: {
  fullscreen: boolean
  onToggle: () => void
}) {
  const label = fullscreen ? 'Exit fullscreen' : 'Fullscreen'
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Button variant="ghost" size="icon-xs" aria-label={label} onClick={onToggle}>
          <HugeiconsIcon icon={fullscreen ? ArrowShrink01Icon : FullScreenIcon} />
        </Button>
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
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
      {/* The panel re-renders its own header inside the dialog, and its
          FullscreenToggle (now a minimize button) sits exactly where
          DialogContent's built-in close X lands — suppress the X so the
          two don't overlap; Escape and the toggle both still exit. */}
      <DialogContent
        showCloseButton={false}
        className="flex h-[calc(100vh-4rem)] max-w-[calc(100vw-4rem)] flex-col p-0 sm:max-w-[calc(100vw-4rem)]"
      >
        {children}
      </DialogContent>
    </Dialog>
  )
}
