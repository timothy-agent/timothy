import {
  Analytics01Icon,
  InboxIcon,
  RocketIcon,
  Settings02Icon,
} from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useState } from 'react'
import { useNavigate } from 'react-router'
import { useAgents } from '../components/AgentPicker'
import { Composer, type PendingAttachment } from '../components/Composer'
import { Eyebrow } from '../components/timothy/page-header'
import { PageShell } from '../components/timothy/page-shell'
import { EmptyState } from '../components/timothy/empty-state'
import { Badge } from '../components/ui/badge'
import { Card } from '../components/ui/card'
import { usePendingMemories } from '../lib/memory'

const agentKey = 'timothy.agent'
const routeKey = 'timothy.route'

// ChatIntent carries a home-screen action into the chat page: `send`
// fires immediately, `draft` only prefills the composer, `skillHint`
// pins a skill chip (deterministic — never text the user can mangle).
// `attachments` rides alongside `send` only — images uploaded from the
// home composer before Chat.tsx even mounts.
export interface ChatIntent {
  send?: string
  draft?: string
  agent?: string
  route?: string
  skillHint?: string
  attachments?: PendingAttachment[]
  knowledge?: string[]
}

// Home is the workspace launcher: a hero composer that starts a chat,
// and the agent picker as the actual capability map — agents-first,
// since who answers (their prompt, route, skills, tools, memory) is
// the thing that varies, not a fixed set of feature tiles.
export function Home() {
  const navigate = useNavigate()
  const pending = usePendingMemories()
  const agents = useAgents()
  const [draft, setDraft] = useState('')
  const [agent, setAgent] = useState(() => localStorage.getItem(agentKey) ?? '')
  const [route, setRoute] = useState(() => localStorage.getItem(routeKey) ?? '')
  const [attachments, setAttachments] = useState<PendingAttachment[]>([])
  const [knowledge, setKnowledge] = useState<string[]>([])
  // Same fallback as AgentRoutePicker: an empty/unmatched agent name
  // resolves to the default agent, the one that actually serves it.
  const servingAgent = agents.find((a) => a.name === agent) ?? agents.find((a) => a.is_default)
  const agentKnowledge = servingAgent?.knowledge ?? []

  const pickAgent = (a: string) => {
    setAgent(a)
    localStorage.setItem(agentKey, a)
  }

  const pickRoute = (r: string) => {
    setRoute(r)
    localStorage.setItem(routeKey, r)
  }

  const send = () => {
    const message = draft.trim()
    const ready = attachments.filter((a) => !a.uploading)
    if (!message && ready.length === 0) return
    navigate('/chat', {
      state: {
        send: message,
        agent,
        route: route || undefined,
        attachments: ready.length > 0 ? ready : undefined,
        knowledge: knowledge.length > 0 ? knowledge : undefined,
      } satisfies ChatIntent,
    })
  }

  const openAgent = (name: string) => {
    pickAgent(name)
    navigate('/chat', { state: { agent: name } satisfies ChatIntent })
  }

  return (
    <PageShell className="flex h-full flex-col items-center overflow-y-auto">
      <div className="mt-[max(3rem,12vh)] w-full max-w-4xl text-center">
        <h1 className="text-display font-semibold text-foreground">Timothy</h1>
        <p className="mt-2 text-sm text-muted-foreground">
          Ask anything. Chats remember what matters.
        </p>
        <div className="mt-8 text-left">
          <Composer
            draft={draft}
            onDraft={setDraft}
            onSend={send}
            agent={agent}
            onAgent={pickAgent}
            route={route}
            onRoute={pickRoute}
            autoFocus
            placeholder="Ask anything…"
            attachments={attachments}
            onAttachments={setAttachments}
            knowledge={knowledge}
            onKnowledge={setKnowledge}
            agentKnowledge={agentKnowledge}
          />
        </div>
      </div>

      <div className="mt-14 w-full max-w-4xl">
        <h2 className="text-center">
          <Eyebrow>Agents</Eyebrow>
        </h2>
        <div className="mt-5 grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {agents.map((a) => (
            <Card
              key={a.id}
              interactive
              asChild
              className="flex flex-col gap-2 text-left"
            >
              <button type="button" onClick={() => openAgent(a.name)} aria-label={a.name}>
                <div className="flex items-center gap-2">
                  <span className="truncate text-sm font-semibold capitalize">{a.name}</span>
                  {a.is_default && (
                    <Badge variant="brand" size="sm">
                      Default
                    </Badge>
                  )}
                </div>
                {a.description && (
                  <p className="line-clamp-2 text-sm text-muted-foreground">{a.description}</p>
                )}
              </button>
            </Card>
          ))}
          {agents.length === 0 && (
            <div className="col-span-full rounded-md border border-dashed border-border">
              <EmptyState title="No agents configured yet, the default agent will serve new chats." />
            </div>
          )}
        </div>
      </div>

      <div className="mt-10 mb-10 flex items-center gap-8">
        <button
          type="button"
          onClick={() => navigate('/memory')}
          className="group flex flex-col items-center gap-2"
        >
          <span className="relative flex size-11 items-center justify-center rounded-md border border-transparent text-muted-foreground transition group-hover:border-border group-hover:bg-muted">
            <HugeiconsIcon icon={InboxIcon} className="size-5.5" />
            {pending > 0 && (
              <span className="absolute -top-1 -right-1 flex h-4 min-w-4 items-center justify-center rounded-full bg-brand px-1 text-xs font-medium text-brand-foreground">
                {pending}
              </span>
            )}
          </span>
          <span className="text-xs text-muted-foreground">Memory</span>
        </button>
        <button
          type="button"
          onClick={() => navigate('/missions')}
          className="group flex flex-col items-center gap-2"
        >
          <span className="flex size-11 items-center justify-center rounded-md border border-transparent text-muted-foreground transition group-hover:border-border group-hover:bg-muted">
            <HugeiconsIcon icon={RocketIcon} className="size-5.5" />
          </span>
          <span className="text-xs text-muted-foreground">Missions</span>
        </button>
        <button
          type="button"
          onClick={() => navigate('/analytics')}
          className="group flex flex-col items-center gap-2"
        >
          <span className="flex size-11 items-center justify-center rounded-md border border-transparent text-muted-foreground transition group-hover:border-border group-hover:bg-muted">
            <HugeiconsIcon icon={Analytics01Icon} className="size-5.5" />
          </span>
          <span className="text-xs text-muted-foreground">Analytics</span>
        </button>
        <button
          type="button"
          onClick={() => navigate('/settings')}
          className="group flex flex-col items-center gap-2"
        >
          <span className="flex size-11 items-center justify-center rounded-md border border-transparent text-muted-foreground transition group-hover:border-border group-hover:bg-muted">
            <HugeiconsIcon icon={Settings02Icon} className="size-5.5" />
          </span>
          <span className="text-xs text-muted-foreground">Settings</span>
        </button>
      </div>
    </PageShell>
  )
}
