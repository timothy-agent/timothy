import { Chat } from './Chat'

// Research is a dedicated, single-purpose entry point: the
// research-brief skill is pinned for every turn (not removable, not
// picked from a chip) so every answer follows the same source-grounded
// discipline — search_web + fetch_url, cited claims, a structured
// Sources list the UI renders as its own panel.
export function Research({ onNeedToken }: { onNeedToken: () => void }) {
  return (
    <Chat
      onNeedToken={onNeedToken}
      basePath="/research"
      lockedSkillHint="research-brief"
      emptyHeading="What do you want to research?"
      emptySubtext="Ask a question that needs current facts, Timothy searches, reads sources, and cites everything it claims."
      placeholder="Ask a research question…"
    />
  )
}
