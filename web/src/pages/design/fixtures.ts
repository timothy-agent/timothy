// Realistic showcase content for the design-system route. No lorem
// ipsum: every string reads like something Timothy would actually
// produce or store.

export const missionGoal =
  'Collect unread GitHub notifications from the last 7 days across timothy-agent/timothy and timothy-agent/pounce, group them by repository, and deliver a Markdown digest by email every Monday at 08:00 Europe/Amsterdam.'

export const timothyAnswerParagraphs = [
  'I searched the web with `search_web` for recent guidance on chunking strategies before touching the ingest pipeline, since the current splitter cuts mid-sentence on long paragraphs.',
  'The retrieval path already stores embeddings in pgvector and fuses vector, text and entity matches with RRF, so a better chunker should raise recall without changing the retrieval code at all.',
  'I ran a small benchmark against last week\'s knowledge base documents; the change adds about $0.0042 in embedding cost per re-ingested collection, which is small enough to apply by default.',
]

export const toolCallArgs = {
  tool: 'search_web',
  query: 'pgvector hybrid retrieval RRF chunking best practices 2026',
  limit: 8,
  results: [
    { title: 'Reciprocal rank fusion for hybrid search', url: 'https://example.com/rrf-hybrid-search', snippet: 'Combining vector and lexical rankings with RRF avoids tuning a single blended score.' },
    { title: 'pgvector 0.8 performance notes', url: 'https://example.com/pgvector-0-8', snippet: 'HNSW index build time drops roughly 30% with the new parallel builder.' },
    { title: 'Chunking strategies for long documents', url: 'https://example.com/chunking-strategies', snippet: 'Sentence-aware splitting with overlap outperforms fixed-token windows on recall.' },
    { title: 'Evaluating RAG systems without a labeled set', url: 'https://example.com/rag-eval-unlabeled', snippet: 'Synthetic query generation from source documents gives a usable eval set in an afternoon.' },
    { title: 'When to re-embed after a chunker change', url: 'https://example.com/re-embed-chunker-change', snippet: 'Re-embedding only the changed collections keeps the cost bounded.' },
    { title: 'Entity extraction for retrieval', url: 'https://example.com/entity-extraction-retrieval', snippet: 'Named entities as a third retrieval signal help short, keyword-heavy queries.' },
    { title: 'Postgres full-text search vs dedicated search engines', url: 'https://example.com/postgres-fts-vs-search-engines', snippet: 'For under a few million rows, Postgres FTS keeps operational surface area small.' },
    { title: 'Cost of embedding models per Mtok, 2026', url: 'https://example.com/embedding-cost-2026', snippet: 'Small embedding models now cost a fraction of a cent per million tokens.' },
  ],
  options: {
    region: 'auto',
    safe_search: true,
    freshness: 'week',
  },
  requested_by: 'mission_worker',
  requested_at: '2026-09-07T08:03:12Z',
}

export interface EventLogRow {
  time: string
  kind: 'tool' | 'plan' | 'phase' | 'note' | 'review'
  text: string
  status?: 'neutral' | 'working' | 'waiting' | 'success' | 'warning' | 'error'
}

export const eventLogRows: EventLogRow[] = [
  { time: '2026-09-07T08:00:02Z', kind: 'phase', text: 'Mission started', status: 'working' },
  { time: '2026-09-07T08:00:14Z', kind: 'plan', text: 'Plan approved, 3 units', status: 'success' },
  { time: '2026-09-07T08:01:03Z', kind: 'tool', text: 'search_web "unread github notifications api"', status: 'success' },
  { time: '2026-09-07T08:01:41Z', kind: 'tool', text: 'list_calendar_events account=work', status: 'success' },
  { time: '2026-09-07T08:02:20Z', kind: 'note', text: 'Grouped 14 notifications across 2 repositories' },
  { time: '2026-09-07T08:02:55Z', kind: 'tool', text: 'read_file digest-template.md', status: 'success' },
  { time: '2026-09-07T08:03:30Z', kind: 'review', text: 'Prove round: 2 of 2 units harness-passed', status: 'success' },
  { time: '2026-09-07T08:04:02Z', kind: 'tool', text: 'send_mail account=personal', status: 'working' },
  { time: '2026-09-07T08:04:09Z', kind: 'tool', text: 'send_mail account=personal', status: 'success' },
  { time: '2026-09-07T08:04:11Z', kind: 'phase', text: 'Mission done', status: 'success' },
]

// eventLogComponentRows feeds the new timothy/event-log.tsx EventLog
// component's showcase section (distinct shape from the legacy
// eventLogRows above, which the hand-rolled EventLogSample still
// uses). Plain data only, no JSX, since this file has no .tsx JSX
// support: AgentSurfaces.tsx builds ReactNode titles/payloads for
// these rows itself.
export interface EventLogComponentRowFixture {
  id: string
  time: string
  kind: 'phase' | 'plan' | 'tool' | 'note' | 'turn' | 'review'
  status?: 'neutral' | 'working' | 'waiting' | 'success' | 'warning' | 'error'
  text: string
  target?: string
  payload?: unknown
}

export const eventLogComponentRows: EventLogComponentRowFixture[] = [
  { id: 'started', time: '2026-09-07T08:00:02Z', kind: 'phase', status: 'working', text: 'Mission started' },
  { id: 'plan-approved', time: '2026-09-07T08:00:14Z', kind: 'plan', status: 'success', text: 'Plan approved, 3 units' },
  {
    id: 'search-web',
    time: '2026-09-07T08:01:03Z',
    kind: 'tool',
    status: 'success',
    text: 'search_web',
    target: 'unread github notifications api',
    payload: { query: 'unread github notifications api', limit: 8 },
  },
  {
    id: 'calendar',
    time: '2026-09-07T08:01:41Z',
    kind: 'tool',
    status: 'success',
    text: 'list_calendar_events',
    target: 'account=work',
  },
  { id: 'note', time: '2026-09-07T08:02:20Z', kind: 'note', text: 'Grouped 14 notifications across 2 repositories' },
  { id: 'turn', time: '2026-09-07T08:02:55Z', kind: 'turn', status: 'success', text: 'Turn: gathered notifications' },
  { id: 'review', time: '2026-09-07T08:03:30Z', kind: 'review', status: 'success', text: 'Prove round: 2 of 2 units harness-passed' },
  { id: 'done', time: '2026-09-07T08:04:11Z', kind: 'phase', status: 'success', text: 'Mission done' },
]

export interface ModelOption {
  id: string
  provider: string
  pricePerMtok: string
}

export const models: ModelOption[] = [
  { id: 'gpt-5.2-mini', provider: 'OpenAI', pricePerMtok: '$0.25 / $1.00' },
  { id: 'claude-sonnet-5', provider: 'Anthropic', pricePerMtok: '$3.00 / $15.00' },
  { id: 'claude-opus-5', provider: 'Anthropic', pricePerMtok: '$15.00 / $75.00' },
  { id: 'qwen3:4b', provider: 'Ollama (local)', pricePerMtok: 'price unknown' },
  { id: 'glm-4.6', provider: 'Zhipu', pricePerMtok: '$0.60 / $2.20' },
  { id: 'gemini-2.5-flash', provider: 'Google', pricePerMtok: '$0.15 / $0.60' },
]

export interface ProviderRecord {
  name: string
  status: 'success' | 'warning' | 'error'
  statusLabel: string
  lastChecked: string
}

export const providers: ProviderRecord[] = [
  { name: 'OpenAI', status: 'success', statusLabel: 'Healthy', lastChecked: '2 min ago' },
  { name: 'Ollama (local)', status: 'warning', statusLabel: 'Degraded', lastChecked: '5 min ago' },
]
