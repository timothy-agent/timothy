import {
  Attachment02Icon,
  ArrowDown01Icon,
  ArrowUp01Icon,
  Cancel01Icon,
  FileMusicIcon,
  FileVideoIcon,
  Loading03Icon,
  Mic01Icon,
  Pdf02Icon,
  StopIcon,
  Tick02Icon,
} from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import type { ClipboardEvent, DragEvent, KeyboardEvent, ReactNode } from 'react'
import { useEffect, useRef, useState } from 'react'
import { toast } from 'sonner'
import { getSettings, listKbCollections, transcribe, uploadAttachment } from '../api/client'
import type { KbCollection, Reference } from '../api/types'
import {
  addReference,
  groupReferenceOptions,
  maxReferences,
  referenceKindLabel,
  removeReference,
  useReferenceSearch,
} from '../lib/references'
import { skillLabels } from '../lib/skills'
import {
  getTranscribeLanguage,
  setTranscribeLanguage,
  TRANSCRIBE_LANGUAGES,
} from '../lib/transcribeLanguage'
import { AgentRoutePicker } from './AgentRoutePicker'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from './ui/dropdown-menu'

// PendingAttachment is one upload in flight or done, owned by the
// page (same pattern as `draft`) so the send flow can read and clear
// it. previewUrl is a local object URL: revoked on removal/send.
export interface PendingAttachment {
  id: string
  mime: string
  previewUrl: string
  name?: string
  uploading?: boolean
}

const allowedMimes = [
  'image/png',
  'image/jpeg',
  'image/webp',
  'image/gif',
  'application/pdf',
  'text/plain',
  'text/markdown',
  'video/mp4',
  'video/webm',
  'audio/mpeg',
  'audio/wav',
  'audio/ogg',
]
// Exported: MissionAttachments.tsx reuses the same caps for its own
// document upload flow.
export const maxAttachmentBytes = 10 * 1024 * 1024
export const maxVideoBytes = 100 * 1024 * 1024
export const maxAudioBytes = 25 * 1024 * 1024
export const maxAttachments = 8

// maxBytesFor mirrors the server's per-type size caps (internal/brain/
// attachments) so an oversize file is rejected client-side before an
// upload round-trip.
function maxBytesFor(mime: string): number {
  if (mime.startsWith('video/')) return maxVideoBytes
  if (mime.startsWith('audio/')) return maxAudioBytes
  return maxAttachmentBytes
}

// documentChipIcon picks the composer's pending-chip icon by mime.
function documentChipIcon(mime: string) {
  if (mime.startsWith('video/')) return FileVideoIcon
  if (mime.startsWith('audio/')) return FileMusicIcon
  return Pdf02Icon
}

// isAllowedFile accepts a file when its reported type is in
// allowedMimes, or (for .md/.txt) by extension: browsers report
// markdown files inconsistently: empty type, text/markdown, or
// text/plain depending on OS/browser.
export function isAllowedFile(file: File): boolean {
  if (allowedMimes.includes(file.type)) return true
  return /\.(md|txt)$/i.test(file.name)
}

// isDocumentFile is isAllowedFile narrowed to document types (PDF,
// Markdown, text): used where only documents are accepted.
export function isDocumentFile(file: File): boolean {
  return (
    isAllowedFile(file) &&
    !file.type.startsWith('image/') &&
    !file.type.startsWith('video/') &&
    !file.type.startsWith('audio/')
  )
}

// isMissionAttachmentFile is isAllowedFile narrowed to what missions
// accept (issue #359): documents, images, and audio. Video stays
// chat-only (explicit decision): excluded here even though
// allowedMimes carries it for the composer.
export function isMissionAttachmentFile(file: File): boolean {
  return isAllowedFile(file) && !file.type.startsWith('video/')
}

// isDocumentAttachment mirrors the server's document (non-image)
// attachments: PDF, text/markdown, video and audio files render as a
// document chip rather than an image thumbnail.
export function isDocumentAttachment(mime: string): boolean {
  return (
    mime === 'application/pdf' ||
    mime === 'text/plain' ||
    mime === 'text/markdown' ||
    mime.startsWith('video/') ||
    mime.startsWith('audio/')
  )
}

// Composer is the one message box: the chat page's docked input and
// the home page's hero input are the same component so behavior
// (autogrow, Enter-to-send, agent pill) never drifts.
export function Composer({
  draft,
  onDraft,
  onSend,
  agent,
  onAgent,
  route,
  onRoute,
  hidePicker = false,
  skillHint,
  onRemoveSkillHint,
  disabled = false,
  streaming = false,
  onStop,
  autoFocus = false,
  placeholder = 'Message Timothy…',
  attachments = [],
  onAttachments,
  knowledge,
  onKnowledge,
  agentKnowledge,
  references,
  onReferences,
}: {
  draft: string
  onDraft: (v: string) => void
  onSend: () => void
  agent: string
  onAgent: (a: string) => void
  route?: string
  onRoute?: (r: string) => void
  hidePicker?: boolean
  skillHint?: string
  onRemoveSkillHint?: () => void
  disabled?: boolean
  // streaming/onStop swap the send button for a stop control while a
  // turn is in flight: the turn now runs detached server-side (see
  // chat.Service.StopTurn), so unmounting or navigating away no longer
  // stops it; this is the explicit way to.
  streaming?: boolean
  onStop?: () => void
  autoFocus?: boolean
  placeholder?: string
  attachments?: PendingAttachment[]
  onAttachments?: (next: PendingAttachment[]) => void
  // knowledge/onKnowledge enable `#collection` mentions: pinned kb
  // collection names shown as chips. Omitting them disables mentions
  // for that composer instance.
  knowledge?: string[]
  onKnowledge?: (next: string[]) => void
  // Collections the serving agent always searches (agents' `knowledge`
  // field): shown as muted, non-removable chips and excluded from the
  // mention popup since pinning them is redundant.
  agentKnowledge?: string[]
  // references/onReferences enable `#mission`/`#chat`/`#document`
  // mentions alongside `#collection` in the same popup: picking one
  // inserts a removable chip, capped at maxReferences. Omitting them
  // disables reference mentions for that composer instance.
  references?: Reference[]
  onReferences?: (next: Reference[]) => void
}) {
  const inputRef = useRef<HTMLTextAreaElement>(null)
  const fileInputRef = useRef<HTMLInputElement>(null)
  const recorderRef = useRef<MediaRecorder | null>(null)
  const chunksRef = useRef<BlobPart[]>([])
  // The attachments array is owned by the page, but the upload flow
  // needs the latest value inside async callbacks that started before
  // a later render: same stale-closure guard as draftRef below.
  const attachmentsRef = useRef(attachments)
  attachmentsRef.current = attachments
  // The recorder's onstop closure outlives renders; read the draft
  // through a ref so a transcript appends to what the user typed
  // DURING the recording, not to a stale snapshot from record-start.
  const draftRef = useRef(draft)
  draftRef.current = draft
  const [recording, setRecording] = useState(false)
  const [transcribing, setTranscribing] = useState(false)
  const [transcribeLanguage, setTranscribeLanguageState] = useState(getTranscribeLanguage)
  // Default hidden: a mic that 404s because WHISPER_URL is unset is
  // worse than one that appears a beat late once the settings fetch
  // resolves.
  const [transcribeEnabled, setTranscribeEnabled] = useState(false)

  useEffect(() => {
    getSettings()
      .then((s) => setTranscribeEnabled(s.settings.transcribe_enabled ?? false))
      .catch(() => setTranscribeEnabled(false))
  }, [])

  // #collection mention state. Collections are fetched lazily (once)
  // the first time the user types `#`, then cached for the rest of
  // this component's lifetime.
  const collectionsRef = useRef<KbCollection[] | null>(null)
  const [mentionQuery, setMentionQuery] = useState<string | null>(null)
  const [mentionOptions, setMentionOptions] = useState<KbCollection[]>([])
  const [mentionIndex, setMentionIndex] = useState(0)
  // The knowledge array the mention popup filters against: read
  // through a ref so the async collections fetch and keydown handler
  // (closures that can outlive a render) see the latest chip list.
  const knowledgeRef = useRef(knowledge ?? [])
  knowledgeRef.current = knowledge ?? []
  // Same stale-closure guard for the agent-bound list: excluded from
  // the popup (pinning them is redundant), read through a ref for the
  // same reason as knowledgeRef above.
  const agentKnowledgeRef = useRef(agentKnowledge ?? [])
  agentKnowledgeRef.current = agentKnowledge ?? []
  // Whether a collections fetch is currently in flight, separate from
  // collectionsRef's data so a failure can reset the data ref to null
  // (retry on the next `#`) without also racing a second fetch.
  const collectionsFetchingRef = useRef(false)

  // #mission/#chat/#document reference mention state: generalizes the
  // collection mention above onto individual missions/chats/kb
  // documents (D-069-style additive feature, same `#` trigger, same
  // popup). referenceQuery mirrors mentionQuery but debounced through
  // useReferenceSearch, since these hit three list/search endpoints
  // per keystroke pause rather than one cached-after-first-fetch list.
  const [referenceQuery, setReferenceQuery] = useState<string | null>(null)
  const referenceOptionsRaw = useReferenceSearch(onReferences ? referenceQuery : null)
  const referencesRef = useRef(references ?? [])
  referencesRef.current = references ?? []
  const referenceOptions = referenceOptionsRaw.filter(
    (o) => !referencesRef.current.some((r) => r.kind === o.kind && r.id === o.id),
  )
  const referenceGroups = groupReferenceOptions(referenceOptions)
  const atCap = (references ?? []).length >= maxReferences

  const mentionRe = /(^|\s)#([a-zA-Z0-9_-]*)$/

  function optionClassName(highlighted: boolean): string {
    return highlighted
      ? 'block w-full truncate px-3 py-1.5 text-left text-sm bg-zinc-100 text-zinc-900 dark:bg-zinc-700 dark:text-white'
      : 'block w-full truncate px-3 py-1.5 text-left text-sm text-zinc-700 hover:bg-zinc-50 dark:text-zinc-200 dark:hover:bg-zinc-700/50'
  }

  function filterOptions(cols: KbCollection[], query: string): KbCollection[] {
    return cols.filter(
      (c) =>
        c.name.toLowerCase().includes(query) &&
        !knowledgeRef.current.includes(c.name) &&
        !agentKnowledgeRef.current.includes(c.name),
    )
  }

  // updateMention re-derives the popup from the text before the caret
  // on every draft/selection change. Only active when onKnowledge or
  // onReferences is given: mentions are opt-in per composer instance.
  function updateMention(text: string, caret: number) {
    if (!onKnowledge && !onReferences) return
    const before = text.slice(0, caret)
    const match = mentionRe.exec(before)
    if (!match) {
      setMentionQuery(null)
      setReferenceQuery(null)
      return
    }
    const query = match[2].toLowerCase()
    setMentionIndex(0)
    if (onReferences) setReferenceQuery(query)
    if (!onKnowledge) return
    setMentionQuery(query)
    if (collectionsRef.current === null) {
      if (collectionsFetchingRef.current) return
      collectionsFetchingRef.current = true
      listKbCollections()
        .then((cols) => {
          collectionsRef.current = cols
          setMentionOptions(filterOptions(cols, query))
        })
        .catch(() => {
          // Reset to null (not []) so the next `#` keystroke retries
          // instead of leaving the popup dead until a page reload.
          collectionsRef.current = null
          setMentionOptions([])
        })
        .finally(() => {
          collectionsFetchingRef.current = false
        })
      return
    }
    setMentionOptions(filterOptions(collectionsRef.current, query))
  }

  // clearMentionToken strips the `#prefix` token from the draft and
  // closes both popups: shared by selectMention and selectReference.
  function clearMentionToken(): boolean {
    const el = inputRef.current
    if (!el) return false
    const caret = el.selectionStart ?? draft.length
    const before = draft.slice(0, caret)
    const match = mentionRe.exec(before)
    if (!match) return false
    const start = caret - match[2].length - 1
    const next = draft.slice(0, start) + draft.slice(caret)
    onDraft(next)
    setMentionQuery(null)
    setReferenceQuery(null)
    requestAnimationFrame(() => el.setSelectionRange(start, start))
    return true
  }

  // selectMention strips the `#prefix` token from the draft and adds
  // the collection name as a chip.
  function selectMention(name: string) {
    if (!onKnowledge) return
    if (!clearMentionToken()) return
    if (!(knowledge ?? []).includes(name)) onKnowledge([...(knowledge ?? []), name])
  }

  function removeKnowledge(name: string) {
    onKnowledge?.((knowledge ?? []).filter((n) => n !== name))
  }

  // selectReference adds a mission/chat/document reference chip, no-op
  // past maxReferences (the popup already hides options at the cap, so
  // this only guards a race).
  function selectReference(option: { kind: Reference['kind']; id: string; name: string }) {
    if (!onReferences || atCap) return
    if (!clearMentionToken()) return
    onReferences(addReference(references ?? [], option))
  }

  function removeReferenceChip(kind: Reference['kind'], id: string) {
    onReferences?.(removeReference(references ?? [], kind, id))
  }

  // combinedOptions flattens the collection list and the grouped
  // reference lists into one selectable sequence, collections first:
  // ArrowUp/ArrowDown, Enter/Tab, and Escape all operate on this single
  // index regardless of which source an option came from. header is
  // set on a group's first row only, so the popup can render a section
  // label without breaking the flat index math keyboard nav relies on.
  const combinedOptions: {
    select: () => void
    header?: string
    render: (highlighted: boolean) => ReactNode
  }[] = []
  if (onKnowledge) {
    for (const c of mentionOptions) {
      combinedOptions.push({
        select: () => selectMention(c.name),
        render: (highlighted) => (
          <button
            type="button"
            onMouseDown={(e) => e.preventDefault()}
            onClick={() => selectMention(c.name)}
            className={optionClassName(highlighted)}
          >
            #{c.name}
          </button>
        ),
      })
    }
  }
  if (onReferences && !atCap) {
    for (const group of referenceGroups) {
      group.options.forEach((o, i) => {
        combinedOptions.push({
          select: () => selectReference(o),
          header: i === 0 ? referenceKindLabel[group.kind] : undefined,
          render: (highlighted) => (
            <button
              type="button"
              onMouseDown={(e) => e.preventDefault()}
              onClick={() => selectReference(o)}
              className={optionClassName(highlighted)}
            >
              {o.name}
            </button>
          ),
        })
      })
    }
  }

  const popupOpen = combinedOptions.length > 0 && (mentionQuery !== null || referenceQuery !== null)

  function handleMentionKeyDown(e: KeyboardEvent<HTMLTextAreaElement>): boolean {
    if (!popupOpen) {
      if (e.key === 'Escape' && (mentionQuery !== null || referenceQuery !== null)) {
        e.preventDefault()
        setMentionQuery(null)
        setReferenceQuery(null)
        return true
      }
      return false
    }
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      setMentionIndex((i) => (i + 1) % combinedOptions.length)
      return true
    }
    if (e.key === 'ArrowUp') {
      e.preventDefault()
      setMentionIndex((i) => (i - 1 + combinedOptions.length) % combinedOptions.length)
      return true
    }
    if (e.key === 'Enter' || e.key === 'Tab') {
      e.preventDefault()
      combinedOptions[Math.min(mentionIndex, combinedOptions.length - 1)].select()
      return true
    }
    if (e.key === 'Escape') {
      e.preventDefault()
      setMentionQuery(null)
      setReferenceQuery(null)
      return true
    }
    return false
  }

  // Auto-grow up to a cap, then scroll inside. Runs on every draft
  // change so programmatic clears (post-send) shrink it back.
  useEffect(() => {
    const el = inputRef.current
    if (!el) return
    el.style.height = 'auto'
    el.style.height = `${Math.min(el.scrollHeight, 200)}px`
  }, [draft])

  // startRecording/stopRecording bracket one mic capture: click starts
  // it, click again (or unmount) stops it and hands the recorded blob
  // to /v1/transcribe. Audio never leaves the box beyond that call.
  async function startRecording() {
    if (!navigator.mediaDevices?.getUserMedia || typeof MediaRecorder === 'undefined') {
      toast.error('Microphone input is not available in this browser or context')
      return
    }
    let stream: MediaStream
    try {
      stream = await navigator.mediaDevices.getUserMedia({ audio: true })
    } catch {
      toast.error('Microphone permission was denied')
      return
    }
    const mimeType = MediaRecorder.isTypeSupported('audio/webm;codecs=opus')
      ? 'audio/webm;codecs=opus'
      : ''
    const recorder = mimeType ? new MediaRecorder(stream, { mimeType }) : new MediaRecorder(stream)
    chunksRef.current = []
    recorder.ondataavailable = (e) => {
      if (e.data.size > 0) chunksRef.current.push(e.data)
    }
    recorder.onstop = () => {
      stream.getTracks().forEach((t) => t.stop())
      const blob = new Blob(chunksRef.current, { type: recorder.mimeType || 'audio/webm' })
      chunksRef.current = []
      void transcribeBlob(blob)
    }
    recorderRef.current = recorder
    recorder.start()
    setRecording(true)
  }

  function stopRecording() {
    recorderRef.current?.stop()
    recorderRef.current = null
    setRecording(false)
  }

  async function transcribeBlob(blob: Blob) {
    setTranscribing(true)
    try {
      const text = await transcribe(blob, transcribeLanguage)
      const cur = draftRef.current
      if (text) onDraft(cur ? `${cur} ${text}` : text)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Transcription failed')
    } finally {
      setTranscribing(false)
    }
  }

  // Stop cleanly if the composer unmounts mid-recording (e.g. route
  // change): the MediaRecorder and its track must not keep running.
  useEffect(() => () => recorderRef.current?.stop(), [])

  // uploadFiles validates each file client-side (type, size, cap) then
  // uploads it, adding a chip immediately in an "uploading" state so
  // the spinner overlay has something to render, and swapping it for
  // the real id on success or dropping it on failure. previewUrl uses
  // the local file directly: no need to round-trip through the
  // server just to show a thumbnail.
  async function uploadFiles(files: File[]) {
    if (!onAttachments) return
    for (const file of files) {
      if (!isAllowedFile(file)) {
        toast.error(`${file.name || 'file'}: unsupported file type`)
        continue
      }
      const cap = maxBytesFor(file.type)
      if (file.size > cap) {
        toast.error(`${file.name || 'file'}: exceeds the ${Math.round(cap / (1024 * 1024))}MB limit`)
        continue
      }
      if (attachmentsRef.current.length >= maxAttachments) {
        toast.error(`You can attach up to ${maxAttachments} files`)
        break
      }
      const tempId = crypto.randomUUID()
      const previewUrl = URL.createObjectURL(file)
      const placeholder: PendingAttachment = {
        id: tempId,
        mime: file.type,
        previewUrl,
        name: file.name,
        uploading: true,
      }
      attachmentsRef.current = [...attachmentsRef.current, placeholder]
      onAttachments(attachmentsRef.current)
      try {
        const uploaded = await uploadAttachment(file)
        attachmentsRef.current = attachmentsRef.current.map((a) =>
          a.id === tempId ? { ...a, id: uploaded.id, uploading: false } : a,
        )
        onAttachments(attachmentsRef.current)
      } catch (err) {
        URL.revokeObjectURL(previewUrl)
        attachmentsRef.current = attachmentsRef.current.filter((a) => a.id !== tempId)
        onAttachments(attachmentsRef.current)
        toast.error(err instanceof Error ? err.message : 'Upload failed')
      }
    }
  }

  function removeAttachment(id: string) {
    if (!onAttachments) return
    const target = attachments.find((a) => a.id === id)
    if (target) URL.revokeObjectURL(target.previewUrl)
    onAttachments(attachments.filter((a) => a.id !== id))
  }

  function handlePaste(e: ClipboardEvent<HTMLTextAreaElement>) {
    const files = [...e.clipboardData.items]
      .filter((item) => item.kind === 'file' && item.type.startsWith('image/'))
      .map((item) => item.getAsFile())
      .filter((f): f is File => f !== null)
    if (files.length > 0) void uploadFiles(files)
  }

  function handleDrop(e: DragEvent<HTMLDivElement>) {
    e.preventDefault()
    const files = [...e.dataTransfer.files].filter((f) => f.type.startsWith('image/'))
    if (files.length > 0) void uploadFiles(files)
  }

  // Muted chips: agent-bound collections not already pinned by the
  // user. A name in both wins as the removable violet chip only: no
  // duplicate muted chip alongside it.
  const agentOnlyKnowledge = (agentKnowledge ?? []).filter((name) => !(knowledge ?? []).includes(name))

  return (
    <div
      onDragOver={(e) => e.preventDefault()}
      onDrop={handleDrop}
      className="relative rounded-2xl border border-zinc-950/10 bg-white shadow-sm transition focus-within:border-blue-500/50 focus-within:ring-4 focus-within:ring-blue-500/10 dark:border-white/10 dark:bg-zinc-800/60 dark:focus-within:border-blue-400/40"
    >
      {popupOpen && (
        <div className="absolute bottom-full left-2 z-50 mb-1 max-h-72 w-64 overflow-y-auto rounded-lg border border-zinc-950/10 bg-white py-1 shadow-lg dark:border-white/10 dark:bg-zinc-800">
          {combinedOptions.map((opt, i) => (
            <div key={i}>
              {opt.header && (
                <div className="px-3 pt-1.5 pb-0.5 text-[10px] font-semibold tracking-wider text-zinc-400 uppercase dark:text-zinc-500">
                  {opt.header}
                </div>
              )}
              {opt.render(i === mentionIndex)}
            </div>
          ))}
        </div>
      )}
      {((knowledge && knowledge.length > 0) || agentOnlyKnowledge.length > 0) && (
        <div className="flex flex-wrap items-center gap-1 px-3 pt-2.5">
          {agentOnlyKnowledge.map((name) => (
            <span
              key={`agent-${name}`}
              title="Always searched by this agent"
              className="inline-flex items-center gap-1 rounded-full bg-zinc-100 py-1 px-2.5 text-xs font-medium text-zinc-500 dark:bg-zinc-700/60 dark:text-zinc-400"
            >
              #{name}
            </span>
          ))}
          {knowledge?.map((name) => (
            <span
              key={name}
              className="inline-flex items-center gap-1 rounded-full bg-violet-50 py-1 pr-1.5 pl-2.5 text-xs font-medium text-violet-700 dark:bg-violet-500/10 dark:text-violet-300"
            >
              #{name}
              <button
                type="button"
                onClick={() => removeKnowledge(name)}
                aria-label={`Remove ${name} knowledge`}
                className="flex size-4 items-center justify-center rounded-full text-violet-700/70 hover:bg-violet-100 hover:text-violet-900 dark:text-violet-300/70 dark:hover:bg-violet-500/20 dark:hover:text-violet-100"
              >
                <HugeiconsIcon icon={Cancel01Icon} className="size-3" />
              </button>
            </span>
          ))}
        </div>
      )}
      {references && references.length > 0 && (
        <div className="flex flex-wrap items-center gap-1 px-3 pt-2.5">
          {references.map((r) => (
            <span
              key={`${r.kind}-${r.id}`}
              className="inline-flex max-w-48 items-center gap-1 rounded-full bg-blue-50 py-1 pr-1.5 pl-2.5 text-xs font-medium text-blue-700 dark:bg-blue-500/10 dark:text-blue-300"
            >
              <span className="truncate">
                {referenceKindLabel[r.kind].replace(/s$/, '')}: {r.name}
              </span>
              <button
                type="button"
                onClick={() => removeReferenceChip(r.kind, r.id)}
                aria-label={`Remove ${r.name} reference`}
                className="flex size-4 shrink-0 items-center justify-center rounded-full text-blue-700/70 hover:bg-blue-100 hover:text-blue-900 dark:text-blue-300/70 dark:hover:bg-blue-500/20 dark:hover:text-blue-100"
              >
                <HugeiconsIcon icon={Cancel01Icon} className="size-3" />
              </button>
            </span>
          ))}
        </div>
      )}
      {attachments.length > 0 && (
        <div className="flex flex-wrap gap-2 px-3 pt-2.5">
          {attachments.map((a) => (
            <div
              key={a.id}
              className="group relative size-12 shrink-0 overflow-hidden rounded-lg border border-zinc-950/10 dark:border-white/10"
              title={a.name}
            >
              {isDocumentAttachment(a.mime) ? (
                <div className="flex size-full flex-col items-center justify-center gap-0.5 bg-zinc-100 text-zinc-500 dark:bg-zinc-700 dark:text-zinc-400">
                  <HugeiconsIcon icon={documentChipIcon(a.mime)} className="size-4" />
                  <span className="max-w-full truncate px-1 text-[9px]">{a.name ?? 'Document'}</span>
                </div>
              ) : (
                <img src={a.previewUrl} alt={a.name ?? 'attachment'} className="size-full object-cover" />
              )}
              {a.uploading && (
                <div className="absolute inset-0 flex items-center justify-center bg-black/40">
                  <HugeiconsIcon icon={Loading03Icon} className="size-4 animate-spin text-white" />
                </div>
              )}
              <button
                type="button"
                onClick={() => removeAttachment(a.id)}
                aria-label={`Remove ${a.name ?? 'attachment'}`}
                className="absolute top-0.5 right-0.5 flex size-4 items-center justify-center rounded-full bg-black/60 text-white opacity-0 transition group-hover:opacity-100"
              >
                <HugeiconsIcon icon={Cancel01Icon} className="size-2.5" />
              </button>
            </div>
          ))}
        </div>
      )}
      {skillHint && (
        <div className="flex items-center gap-1 px-3 pt-2.5">
          <span className="inline-flex items-center gap-1 rounded-full bg-blue-50 py-1 pr-1.5 pl-2.5 text-xs font-medium text-blue-700 dark:bg-blue-500/10 dark:text-blue-300">
            {skillLabels[skillHint] ?? skillHint}
            {onRemoveSkillHint && (
              <button
                type="button"
                onClick={onRemoveSkillHint}
                aria-label={`Remove ${skillLabels[skillHint] ?? skillHint} skill`}
                className="flex size-4 items-center justify-center rounded-full text-blue-700/70 hover:bg-blue-100 hover:text-blue-900 dark:text-blue-300/70 dark:hover:bg-blue-500/20 dark:hover:text-blue-100"
              >
                <HugeiconsIcon icon={Cancel01Icon} className="size-3" />
              </button>
            )}
          </span>
        </div>
      )}
      <textarea
        ref={inputRef}
        aria-label="Message"
        rows={1}
        value={draft}
        autoFocus={autoFocus}
        placeholder={placeholder}
        className="max-h-50 w-full resize-none bg-transparent px-4 pt-3.5 pb-1.5 text-base/6 text-zinc-900 outline-none placeholder:text-zinc-400 sm:text-sm/6 dark:text-white dark:placeholder:text-zinc-500"
        onChange={(e) => {
          onDraft(e.target.value)
          updateMention(e.target.value, e.target.selectionStart ?? e.target.value.length)
        }}
        onSelect={(e) => {
          const el = e.currentTarget
          updateMention(el.value, el.selectionStart ?? el.value.length)
        }}
        onPaste={onAttachments ? handlePaste : undefined}
        onKeyDown={(e) => {
          if (handleMentionKeyDown(e)) return
          if (e.key === 'Enter' && !e.shiftKey) {
            e.preventDefault()
            onSend()
          }
        }}
      />
      <div className="flex items-center justify-between gap-2 px-2.5 pb-2.5">
        <div className="flex items-center gap-2">
          {!hidePicker && (
            <AgentRoutePicker agent={agent} onAgent={onAgent} route={route} onRoute={onRoute} />
          )}
          {onAttachments && (
            <>
              <input
                ref={fileInputRef}
                type="file"
                accept="image/png,image/jpeg,image/webp,image/gif,application/pdf,.md,.txt,text/plain,text/markdown,video/mp4,video/webm,audio/mpeg,audio/wav,audio/ogg"
                multiple
                className="hidden"
                onChange={(e) => {
                  const files = [...(e.target.files ?? [])]
                  e.target.value = ''
                  if (files.length > 0) void uploadFiles(files)
                }}
              />
              <button
                type="button"
                onClick={() => fileInputRef.current?.click()}
                aria-label="Attach image"
                disabled={disabled}
                className="flex size-8 shrink-0 items-center justify-center rounded-full text-zinc-500 transition hover:bg-zinc-100 disabled:text-zinc-300 dark:text-zinc-400 dark:hover:bg-zinc-700/50 dark:disabled:text-zinc-600"
              >
                <HugeiconsIcon icon={Attachment02Icon} className="size-4" />
              </button>
            </>
          )}
          {transcribeEnabled && (
            <>
              <button
                type="button"
                onClick={recording ? stopRecording : startRecording}
                aria-label={recording ? 'Stop recording' : 'Record voice input'}
                aria-pressed={recording}
                disabled={disabled || transcribing}
                className={
                  recording
                    ? 'flex size-8 shrink-0 animate-pulse items-center justify-center rounded-full bg-red-600 text-white transition hover:bg-red-500'
                    : 'flex size-8 shrink-0 items-center justify-center rounded-full text-zinc-500 transition hover:bg-zinc-100 disabled:text-zinc-300 dark:text-zinc-400 dark:hover:bg-zinc-700/50 dark:disabled:text-zinc-600'
                }
              >
                <HugeiconsIcon
                  icon={transcribing ? Loading03Icon : Mic01Icon}
                  className={transcribing ? 'size-4 animate-spin' : 'size-4'}
                />
              </button>
              <DropdownMenu>
                <DropdownMenuTrigger
                  aria-label="Speech input language"
                  disabled={disabled || recording || transcribing}
                  className="flex h-8 items-center gap-1 rounded-full px-2 text-xs text-zinc-500 transition hover:bg-zinc-100 disabled:text-zinc-300 dark:text-zinc-400 dark:hover:bg-zinc-700/50 dark:disabled:text-zinc-600"
                >
                  <span>
                    {TRANSCRIBE_LANGUAGES.find((l) => l.code === transcribeLanguage)?.label ??
                      'Auto'}
                  </span>
                  <HugeiconsIcon icon={ArrowDown01Icon} className="size-3 opacity-60" />
                </DropdownMenuTrigger>
                <DropdownMenuContent align="start" className="w-48 p-1.5">
                  <DropdownMenuItem
                    onSelect={() => {
                      setTranscribeLanguageState('')
                      setTranscribeLanguage('')
                    }}
                    data-selected={transcribeLanguage === '' || undefined}
                    className="justify-between rounded-lg px-2.5 py-1.5 data-selected:bg-zinc-100 dark:data-selected:bg-zinc-800"
                  >
                    <span className="text-sm">Auto-detect</span>
                    {transcribeLanguage === '' && (
                      <HugeiconsIcon icon={Tick02Icon} className="size-4" />
                    )}
                  </DropdownMenuItem>
                  {TRANSCRIBE_LANGUAGES.map((l) => (
                    <DropdownMenuItem
                      key={l.code}
                      onSelect={() => {
                        setTranscribeLanguageState(l.code)
                        setTranscribeLanguage(l.code)
                      }}
                      data-selected={transcribeLanguage === l.code || undefined}
                      className="justify-between rounded-lg px-2.5 py-1.5 data-selected:bg-zinc-100 dark:data-selected:bg-zinc-800"
                    >
                      <span className="text-sm">{l.label}</span>
                      {transcribeLanguage === l.code && (
                        <HugeiconsIcon icon={Tick02Icon} className="size-4" />
                      )}
                    </DropdownMenuItem>
                  ))}
                </DropdownMenuContent>
              </DropdownMenu>
            </>
          )}
        </div>
        {streaming && onStop ? (
          <button
            type="button"
            onClick={onStop}
            aria-label="Stop"
            className="flex size-9 shrink-0 items-center justify-center rounded-full bg-blue-600 text-white transition hover:bg-blue-500"
          >
            <HugeiconsIcon icon={StopIcon} className="size-4" />
          </button>
        ) : (
          <button
            type="button"
            onClick={onSend}
            aria-label="Send"
            disabled={disabled || (draft.trim() === '' && attachments.length === 0)}
            className="flex size-9 shrink-0 items-center justify-center rounded-full bg-blue-600 text-white transition hover:bg-blue-500 disabled:bg-zinc-200 disabled:text-zinc-400 dark:disabled:bg-zinc-700 dark:disabled:text-zinc-500"
          >
            <HugeiconsIcon icon={ArrowUp01Icon} className="size-4" />
          </button>
        )}
      </div>
    </div>
  )
}
