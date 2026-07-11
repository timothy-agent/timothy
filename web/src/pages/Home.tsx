import {
  AirplaneTakeOff01Icon,
  Analytics01Icon,
  Brain02Icon,
  BubbleChatIcon,
  ChartLineData01Icon,
  CodeIcon,
  InboxIcon,
  SearchList01Icon,
} from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useState } from 'react'
import { useNavigate } from 'react-router'
import { Composer } from '../components/Composer'
import { categories } from '../lib/chat'
import { usePendingMemories } from '../lib/memory'

const categoryKey = 'timothy.category'

// ChatIntent carries a home-screen action into the chat page: `send`
// fires immediately, `draft` only prefills the composer, `skillHint`
// pins a skill chip (deterministic — never text the user can mangle).
export interface ChatIntent {
  send?: string
  draft?: string
  category?: string
  skillHint?: string
}

interface Tile {
  label: string
  icon: typeof BubbleChatIcon
  to?: string
  intent?: ChatIntent
  badge?: number
}

// Home is the workspace launcher: a hero composer that starts a chat,
// and the capability map — only what exists today, stubs say so.
export function Home() {
  const navigate = useNavigate()
  const pending = usePendingMemories()
  const [draft, setDraft] = useState('')
  const [category, setCategory] = useState(
    () => localStorage.getItem(categoryKey) ?? categories[0],
  )

  const pickCategory = (c: string) => {
    setCategory(c)
    localStorage.setItem(categoryKey, c)
  }

  const send = () => {
    const message = draft.trim()
    if (!message) return
    navigate('/chat', { state: { send: message, category } satisfies ChatIntent })
  }

  const groups: { title: string; tiles: Tile[] }[] = [
    {
      title: 'Chat',
      tiles: [{ label: 'New chat', icon: BubbleChatIcon, to: '/chat' }],
    },
    {
      title: 'Memory',
      tiles: [
        { label: 'Queue', icon: InboxIcon, to: '/memory', badge: pending },
        { label: 'Browse', icon: Brain02Icon, to: '/memory' },
      ],
    },
    {
      // Skill packs are prompt-focused personas the agent loads on
      // demand; each tile opens a chat pre-aimed at one of them.
      title: 'Skills',
      tiles: [
        {
          label: 'Research',
          icon: SearchList01Icon,
          intent: { skillHint: 'research-brief', category: 'research' },
        },
        {
          label: 'Markets',
          icon: ChartLineData01Icon,
          intent: { skillHint: 'markets-digest', category: 'research' },
        },
        {
          label: 'Travel',
          icon: AirplaneTakeOff01Icon,
          intent: { skillHint: 'travel-planning', category: 'research' },
        },
        {
          label: 'Coding',
          icon: CodeIcon,
          intent: { skillHint: 'coding-task', category: 'coding' },
        },
      ],
    },
    {
      title: 'Workspace',
      tiles: [{ label: 'Dashboard', icon: Analytics01Icon, to: '/dashboard' }],
    },
  ]

  const open = (tile: Tile) => {
    if (tile.intent) navigate('/chat', { state: tile.intent })
    else if (tile.to) navigate(tile.to)
  }

  return (
    <div className="flex h-full flex-col items-center overflow-y-auto px-4">
      <div className="mt-[max(3rem,14vh)] w-full max-w-2xl text-center">
        <h1 className="text-3xl font-semibold tracking-tight sm:text-4xl">Timothy</h1>
        <p className="mt-2 text-sm text-muted-foreground">
          Ask anything. Chats remember what matters.
        </p>
        <div className="mt-8 text-left">
          <Composer
            draft={draft}
            onDraft={setDraft}
            onSend={send}
            category={category}
            onCategory={pickCategory}
            autoFocus
            placeholder="Ask anything…"
          />
        </div>
      </div>

      <div className="mt-14 mb-10 flex w-full max-w-5xl flex-col gap-10 lg:flex-row lg:justify-center lg:gap-0 lg:divide-x lg:divide-border">
        {groups.map((group) => (
          <section key={group.title} className="lg:px-10 lg:first:pl-0 lg:last:pr-0">
            <h2 className="text-center text-xs font-medium tracking-wider text-muted-foreground uppercase">
              {group.title}
            </h2>
            <div className="mt-5 flex flex-wrap justify-center gap-x-8 gap-y-6">
              {group.tiles.map((tile) => (
                <button
                  key={tile.label}
                  type="button"
                  onClick={() => open(tile)}
                  className="group flex w-16 flex-col items-center gap-2"
                >
                  <span className="relative flex size-11 items-center justify-center rounded-xl border border-transparent text-zinc-600 transition group-hover:border-border group-hover:bg-zinc-100 dark:text-zinc-300 dark:group-hover:bg-zinc-800">
                    <HugeiconsIcon icon={tile.icon} className="size-5.5" />
                    {tile.badge != null && tile.badge > 0 && (
                      <span className="absolute -top-1 -right-1 flex h-4 min-w-4 items-center justify-center rounded-full bg-blue-600 px-1 text-[10px] font-medium text-white">
                        {tile.badge}
                      </span>
                    )}
                  </span>
                  <span className="text-xs text-zinc-600 dark:text-zinc-300">{tile.label}</span>
                </button>
              ))}
            </div>
          </section>
        ))}
      </div>
    </div>
  )
}
