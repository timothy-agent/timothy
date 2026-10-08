import { Button } from '../../components/ui/button'

const helloPrompts = [
  'What can you do for me?',
  'Summarize how I should use missions',
  'Remember that I prefer short answers',
]

interface HelloStepProps {
  busy: boolean
  onSend: (text: string) => void
  onExplore: () => void
}

export function HelloStep({ busy, onSend, onExplore }: HelloStepProps) {
  return (
    <div>
      <h1 className="text-xl font-semibold">Say hello</h1>
      <p className="mt-2 text-sm text-muted-foreground">Pick a first message, or write your own later.</p>
      <div className="mt-6 flex flex-wrap gap-2">
        {helloPrompts.map((text) => (
          <Button key={text} variant="outline" disabled={busy} onClick={() => onSend(text)}>
            {text}
          </Button>
        ))}
      </div>
      <Button variant="link" className="mt-6 px-0" disabled={busy} onClick={onExplore}>
        I'll explore on my own
      </Button>
    </div>
  )
}
