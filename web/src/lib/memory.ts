// memoryChangedEvent links the Memory page to the sidebar badge
// without a shared provider: queue actions dispatch, the badge
// listens.
export const memoryChangedEvent = 'timothy:memory-changed'

export function notifyMemoryChanged() {
  window.dispatchEvent(new Event(memoryChangedEvent))
}
