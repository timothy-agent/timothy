import {
  BrainIcon,
  Clock01Icon,
  FlashIcon,
  ListViewIcon,
  Search01Icon,
  SparklesIcon,
} from '@hugeicons-pro/core-stroke-rounded'
import type { categories } from './chat'

export type Category = (typeof categories)[number]

// The picker speaks user language (how fast / how smart / how much),
// not routing plumbing. Values stay the real task categories the
// server routes on — only the presentation is tiered: three everyday
// modes up top, specialized chains behind a divider.
export const categoryMeta: Record<
  Category,
  {
    label: string
    description: string
    cost: string
    icon: typeof BrainIcon
    recommended?: boolean
    specialized?: boolean
  }
> = {
  coding: {
    label: 'Standard',
    description: 'Everyday questions and code on a strong all-round model. Balanced quality and cost.',
    cost: '2x',
    icon: SparklesIcon,
    recommended: true,
  },
  mini: {
    label: 'Fast',
    description: 'Quick answers and small tasks on the cheapest capable model.',
    cost: '1x',
    icon: FlashIcon,
  },
  reasoning: {
    label: 'Deep',
    description: 'Hard multi-step problems on the strongest chain. Slowest and priciest.',
    cost: '3x',
    icon: BrainIcon,
  },
  research: {
    label: 'Research',
    description: 'Consults tools and sources before answering, never from memory alone.',
    cost: '3x',
    icon: Search01Icon,
    specialized: true,
  },
  summarize: {
    label: 'Summarize',
    description: 'Condense long content faithfully on a cheap chain.',
    cost: '1x',
    icon: ListViewIcon,
    specialized: true,
  },
  realtime: {
    label: 'Realtime',
    description: 'Snappy back-and-forth where latency beats depth.',
    cost: '1x',
    icon: Clock01Icon,
    specialized: true,
  },
}

// Picker display order: everyday tiers first, specialized after.
export const categoryOrder: Category[] = [
  'coding',
  'mini',
  'reasoning',
  'research',
  'summarize',
  'realtime',
]
