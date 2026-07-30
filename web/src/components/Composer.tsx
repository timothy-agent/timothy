import {
  ArrowUp01Icon,
  Cancel01Icon,
  Loading03Icon,
  Mic01Icon,
} from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useEffect, useRef, useState } from 'react'
import { toast } from 'sonner'
import { transcribe } from '../api/client'
import { skillLabels } from '../lib/skills'
import { AgentRoutePicker } from './AgentRoutePicker'

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
  autoFocus = false,
  placeholder = 'Message Timothy…',
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
  autoFocus?: boolean
  placeholder?: string
}) {
  const inputRef = useRef<HTMLTextAreaElement>(null)
  const recorderRef = useRef<MediaRecorder | null>(null)
  const chunksRef = useRef<BlobPart[]>([])
  // The recorder's onstop closure outlives renders; read the draft
  // through a ref so a transcript appends to what the user typed
  // DURING the recording, not to a stale snapshot from record-start.
  const draftRef = useRef(draft)
  draftRef.current = draft
  const [recording, setRecording] = useState(false)
  const [transcribing, setTranscribing] = useState(false)

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
      const text = await transcribe(blob)
      const cur = draftRef.current
      if (text) onDraft(cur ? `${cur} ${text}` : text)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Transcription failed')
    } finally {
      setTranscribing(false)
    }
  }

  // Stop cleanly if the composer unmounts mid-recording (e.g. route
  // change) — the MediaRecorder and its track must not keep running.
  useEffect(() => () => recorderRef.current?.stop(), [])

  return (
    <div className="rounded-2xl border border-zinc-950/10 bg-white shadow-sm transition focus-within:border-blue-500/50 focus-within:ring-4 focus-within:ring-blue-500/10 dark:border-white/10 dark:bg-zinc-800/60 dark:focus-within:border-blue-400/40">
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
        onChange={(e) => onDraft(e.target.value)}
        onKeyDown={(e) => {
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
        </div>
        <button
          type="button"
          onClick={onSend}
          aria-label="Send"
          disabled={disabled || draft.trim() === ''}
          className="flex size-9 shrink-0 items-center justify-center rounded-full bg-blue-600 text-white transition hover:bg-blue-500 disabled:bg-zinc-200 disabled:text-zinc-400 dark:disabled:bg-zinc-700 dark:disabled:text-zinc-500"
        >
          <HugeiconsIcon icon={ArrowUp01Icon} className="size-4" />
        </button>
      </div>
    </div>
  )
}
