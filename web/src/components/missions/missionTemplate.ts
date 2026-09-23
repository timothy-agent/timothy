import type { MissionTemplate } from '../../api/types'

// normalizeTemplate drops empty values and fields the kind does not use,
// so the saved action carries only what applies.
export function normalizeTemplate(t: MissionTemplate): MissionTemplate {
  const coding = t.kind === 'coding'
  const light = !coding && !!t.light
  const str = (v: string | undefined) => (v && v.trim() !== '' ? v : undefined)
  return {
    goal: t.goal.trim(),
    name: str(t.name),
    kind: t.kind,
    route: str(t.route),
    review_route: str(t.review_route),
    plan_route: str(t.plan_route),
    max_iterations: t.max_iterations || undefined,
    budget_amount: t.budget_amount || undefined,
    budget_currency: t.budget_amount ? t.budget_currency || 'USD' : undefined,
    auto_approve_tools: t.auto_approve_tools ?? true,
    harness: coding ? str(t.harness) : undefined,
    review_harness: light ? undefined : str(t.review_harness),
    environment: coding ? str(t.environment) : undefined,
    destination_ids: t.destination_ids && t.destination_ids.length > 0 ? t.destination_ids : undefined,
    light: coding ? undefined : light,
    attachments: t.attachments && t.attachments.length > 0 ? t.attachments : undefined,
  }
}
