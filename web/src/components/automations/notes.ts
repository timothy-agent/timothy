// Server limits (internal/brain/automations): note name shape and size.
export const maxNoteBytes = 4096
export const noteNameRe = /^[a-z0-9_-]{1,64}$/

// noteBytes is the UTF-8 size the server counts.
export function noteBytes(content: string): number {
  return new TextEncoder().encode(content).length
}
