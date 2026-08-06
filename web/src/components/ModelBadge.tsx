import { presetForProviderName, ProviderMark } from './settings/ProviderLogo'
import { Badge } from './ui/badge'

// ModelBadge renders a model name with its provider's brand mark, in
// the badge style established by the chat and mission usage rows —
// looks up the preset from an arbitrary provider name (D-034 provider
// rows are user-named, see presetForProviderName) so callers only ever
// pass the raw name/model strings they already have. `children`
// appends after the model name for callers that fold more into the
// same pill (e.g. Message's token/duration text).
export function ModelBadge({
  provider,
  model,
  title,
  children,
}: {
  provider: string | undefined
  model: string
  title?: string
  children?: React.ReactNode
}) {
  return (
    <Badge variant="secondary" title={title}>
      <ProviderMark preset={presetForProviderName(provider)} className="size-3" />
      {model}
      {children}
    </Badge>
  )
}
