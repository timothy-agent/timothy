import { Progress } from '../../ui/progress'

// ScoreBar renders a route entry's score against the best score in
// its chain: the leader fills the track, everything else is relative.
export function ScoreBar({ score, max }: { score?: number; max: number }) {
  if (score === undefined) return null
  const pct = max > 0 ? Math.max(2, Math.round((score / max) * 100)) : 0
  return (
    <div className="flex items-center gap-2">
      <Progress value={pct} className="h-1.5 min-w-0 flex-1" data-testid="score-fill" />
      <span className="font-mono text-xs tabular-nums text-muted-foreground">
        {score.toFixed(2)}
      </span>
    </div>
  )
}
