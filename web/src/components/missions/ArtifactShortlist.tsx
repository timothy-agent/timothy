import { ChevronRight, Play } from 'lucide-react'
import { useNavigate } from 'react-router'
import type { OptionShortlist, ShortlistOption } from '../../lib/optionShortlist'
import { followUpGoal } from '../../lib/optionShortlist'
import { FileMarkdownBlock } from '../FilePreviewBlocks'
import { Button } from '../ui/button'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '../ui/collapsible'

function OptionRow({
  option,
  rank,
  onPick,
}: {
  option: ShortlistOption
  rank: number
  onPick: () => void
}) {
  return (
    <Collapsible className="rounded-md border border-border">
      <div className="flex items-start gap-2 p-3">
        <CollapsibleTrigger className="flex min-w-0 flex-1 items-start gap-2 text-left outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background">
          <ChevronRight
            aria-hidden
            className="mt-0.5 size-4 shrink-0 text-muted-foreground transition-transform duration-100 data-[state=open]:rotate-90"
          />
          <span className="min-w-0 flex-1">
            <span className="block text-sm font-medium">
              <span className="mr-2 tabular-nums text-muted-foreground">{rank}.</span>
              {option.name}
            </span>
            {option.pitch !== '' && (
              <span className="mt-0.5 block text-sm text-muted-foreground">{option.pitch}</span>
            )}
          </span>
        </CollapsibleTrigger>
        <Button variant="outline" size="sm" className="shrink-0" onClick={onPick}>
          <Play aria-hidden />
          Pick
        </Button>
      </div>
      <CollapsibleContent>
        <div className="border-t border-border">
          <FileMarkdownBlock text={option.rawMarkdown} raw={false} />
        </div>
      </CollapsibleContent>
    </Collapsible>
  )
}

// ArtifactShortlist renders a parsed option shortlist: one collapsed
// row per option in file order, then the dropped entries in a single
// collapsed disclosure below.
export function ArtifactShortlist({
  missionId,
  shortlist,
}: {
  missionId: string
  shortlist: OptionShortlist
}) {
  const navigate = useNavigate()
  const pick = (option: ShortlistOption) => {
    navigate(`/missions/new?parent=${encodeURIComponent(missionId)}`, {
      state: { pickedOptionGoal: followUpGoal(option) },
    })
  }
  return (
    <div className="space-y-2 p-3">
      {shortlist.options.map((option, i) => (
        <OptionRow key={option.name} option={option} rank={i + 1} onPick={() => pick(option)} />
      ))}
      {shortlist.dropped && (
        <Collapsible className="rounded-md border border-border">
          <CollapsibleTrigger className="flex w-full items-center gap-2 p-3 text-left outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background">
            <ChevronRight
              aria-hidden
              className="size-4 shrink-0 text-muted-foreground transition-transform duration-100 data-[state=open]:rotate-90"
            />
            <span className="text-sm text-muted-foreground">
              Dropped ({shortlist.dropped.entries.length})
            </span>
          </CollapsibleTrigger>
          <CollapsibleContent>
            <ul className="space-y-1.5 border-t border-border p-3">
              {shortlist.dropped.entries.map((entry, i) => (
                <li key={i} className="text-sm">
                  {entry.name && <span className="font-medium">{entry.name}: </span>}
                  <span className="text-muted-foreground">{entry.reason}</span>
                </li>
              ))}
            </ul>
          </CollapsibleContent>
        </Collapsible>
      )}
    </div>
  )
}
