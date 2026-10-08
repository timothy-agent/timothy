import { providerPresets, type ProviderPreset } from '../../lib/providerPresets'
import { ProviderLogo } from '../../components/timothy/provider-logo'
import { Card } from '../../components/ui/card'

const sourceOrder = ['ollama', 'openai', 'anthropic', 'glm', 'grok', 'bedrock', 'cursor', 'custom']

const sourceCopy: Record<string, { title: string; description: string }> = {
  ollama: { title: 'Local model (Ollama)', description: 'Runs on your own machine, no key needed' },
}

const sources: ProviderPreset[] = sourceOrder
  .map((id) => providerPresets.find((p) => p.id === id))
  .filter((p): p is ProviderPreset => p !== undefined)

export function SourceStep({ onPick }: { onPick: (presetId: string) => void }) {
  return (
    <div>
      <h1 className="text-xl font-semibold">Where should Timothy's model come from?</h1>
      <div className="mt-6 grid gap-4 sm:grid-cols-2">
        {sources.map((preset) => {
          const copy = sourceCopy[preset.id] ?? { title: preset.name, description: preset.description }
          return (
            <Card key={preset.id} asChild className="border-dashed p-0">
              <button
                type="button"
                onClick={() => onPick(preset.id)}
                className="flex items-center gap-3 rounded-md p-4 text-left outline-none transition-colors duration-100 hover:border-foreground/20 hover:bg-muted/40 focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background"
              >
                <ProviderLogo preset={preset} className="size-9" />
                <span className="min-w-0">
                  <span className="block text-sm font-semibold">{copy.title}</span>
                  <span className="block truncate text-sm text-muted-foreground">{copy.description}</span>
                </span>
              </button>
            </Card>
          )
        })}
      </div>
      <p className="mt-4 text-sm text-muted-foreground">You can add more providers later in Settings.</p>
    </div>
  )
}
