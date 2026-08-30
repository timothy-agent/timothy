import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { useState } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Reference } from '../api/types'
import { Composer, type PendingAttachment } from './Composer'

vi.mock('../api/client', () => ({
  listAgents: vi.fn().mockResolvedValue([]),
  listRoutes: vi.fn().mockResolvedValue([]),
  transcribe: vi.fn(),
  uploadAttachment: vi.fn(),
  getSettings: vi.fn().mockResolvedValue({ settings: { transcribe_enabled: true }, values: {} }),
  listKbCollections: vi.fn().mockResolvedValue([]),
  listMissions: vi.fn().mockResolvedValue([]),
  listSessions: vi.fn().mockResolvedValue([]),
  searchKbDocuments: vi.fn().mockResolvedValue([]),
}))

vi.mock('sonner', () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}))

// Minimal MediaRecorder fake: capture the constructed instance so a
// test can drive its ondataavailable/onstop callbacks directly,
// exactly like the real recorder would after start()/stop().
class FakeMediaRecorder {
  static instances: FakeMediaRecorder[] = []
  static isTypeSupported = vi.fn().mockReturnValue(true)
  mimeType = 'audio/webm;codecs=opus'
  ondataavailable: ((e: { data: Blob }) => void) | null = null
  onstop: (() => void) | null = null
  start = vi.fn()
  stop = vi.fn(() => this.onstop?.())

  constructor() {
    FakeMediaRecorder.instances.push(this)
  }
}

function baseProps() {
  return {
    draft: '',
    onDraft: vi.fn(),
    onSend: vi.fn(),
    agent: 'auto',
    onAgent: vi.fn(),
  }
}

const stopTrack = vi.fn()
const fakeStream = { getTracks: () => [{ stop: stopTrack }] } as unknown as MediaStream
const getUserMedia = vi.fn().mockResolvedValue(fakeStream)

beforeEach(() => {
  vi.clearAllMocks()
  localStorage.clear()
  FakeMediaRecorder.instances = []
  vi.stubGlobal('MediaRecorder', FakeMediaRecorder)
  Object.defineProperty(navigator, 'mediaDevices', {
    value: { getUserMedia },
    configurable: true,
  })
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('Composer mic button', () => {
  it('renders a mic button', async () => {
    render(<Composer {...baseProps()} />)
    expect(await screen.findByRole('button', { name: 'Record voice input' })).toBeTruthy()
  })

  it('hides the mic button while the settings fetch is unresolved', async () => {
    const client = await import('../api/client')
    vi.mocked(client.getSettings).mockReturnValueOnce(new Promise(() => {}))
    render(<Composer {...baseProps()} />)
    expect(screen.queryByRole('button', { name: 'Record voice input' })).toBeNull()
  })

  it('hides the mic button when transcription is disabled', async () => {
    const client = await import('../api/client')
    vi.mocked(client.getSettings).mockResolvedValueOnce({
      settings: { transcribe_enabled: false },
      values: {},
    })
    render(<Composer {...baseProps()} />)
    await waitFor(() => expect(client.getSettings).toHaveBeenCalled())
    expect(screen.queryByRole('button', { name: 'Record voice input' })).toBeNull()
  })

  it('shows the mic button when transcription is enabled', async () => {
    render(<Composer {...baseProps()} />)
    expect(await screen.findByRole('button', { name: 'Record voice input' })).toBeTruthy()
  })

  it('records, stops, transcribes, and appends the text to the draft', async () => {
    const client = await import('../api/client')
    vi.mocked(client.transcribe).mockResolvedValue('hello world')
    const onDraft = vi.fn()
    render(<Composer {...baseProps()} onDraft={onDraft} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Record voice input' }))
    await waitFor(() => expect(getUserMedia).toHaveBeenCalledWith({ audio: true }))
    await waitFor(() => expect(FakeMediaRecorder.instances).toHaveLength(1))
    const recorder = FakeMediaRecorder.instances[0]
    expect(recorder.start).toHaveBeenCalled()
    expect(screen.getByRole('button', { name: 'Stop recording' })).toBeTruthy()

    recorder.ondataavailable?.({ data: new Blob(['chunk']) })
    fireEvent.click(screen.getByRole('button', { name: 'Stop recording' }))

    await waitFor(() => expect(client.transcribe).toHaveBeenCalled())
    await waitFor(() => expect(onDraft).toHaveBeenCalledWith('hello world'))
    expect(stopTrack).toHaveBeenCalled()
  })

  it('space-separates the transcript from a non-empty draft', async () => {
    const client = await import('../api/client')
    vi.mocked(client.transcribe).mockResolvedValue('world')
    const onDraft = vi.fn()
    render(<Composer {...baseProps()} draft="hello" onDraft={onDraft} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Record voice input' }))
    await waitFor(() => expect(FakeMediaRecorder.instances).toHaveLength(1))
    fireEvent.click(screen.getByRole('button', { name: 'Stop recording' }))

    await waitFor(() => expect(onDraft).toHaveBeenCalledWith('hello world'))
  })

  it('toasts and leaves the draft untouched when transcription fails', async () => {
    const client = await import('../api/client')
    const { toast } = await import('sonner')
    vi.mocked(client.transcribe).mockRejectedValue(new Error('transcribe failed (502)'))
    const onDraft = vi.fn()
    render(<Composer {...baseProps()} draft="untouched" onDraft={onDraft} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Record voice input' }))
    await waitFor(() => expect(FakeMediaRecorder.instances).toHaveLength(1))
    fireEvent.click(screen.getByRole('button', { name: 'Stop recording' }))

    await waitFor(() => expect(toast.error).toHaveBeenCalledWith('transcribe failed (502)'))
    expect(onDraft).not.toHaveBeenCalled()
  })

  it('passes the selected source language to transcribe and persists it', async () => {
    const client = await import('../api/client')
    vi.mocked(client.transcribe).mockResolvedValue('হ্যালো')
    render(<Composer {...baseProps()} />)

    const trigger = await screen.findByRole('button', { name: 'Speech input language' })
    fireEvent.pointerDown(trigger, { button: 0, pointerId: 1 })
    fireEvent.click(trigger)
    fireEvent.click(await screen.findByText('Bangla'))

    fireEvent.click(await screen.findByRole('button', { name: 'Record voice input' }))
    await waitFor(() => expect(FakeMediaRecorder.instances).toHaveLength(1))
    fireEvent.click(screen.getByRole('button', { name: 'Stop recording' }))

    await waitFor(() => expect(client.transcribe).toHaveBeenCalledWith(expect.anything(), 'bn'))
    expect(localStorage.getItem('timothy.transcribeLanguage')).toBe('bn')
  })

  it('toasts when the microphone permission is denied', async () => {
    const { toast } = await import('sonner')
    getUserMedia.mockRejectedValueOnce(new Error('denied'))
    render(<Composer {...baseProps()} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Record voice input' }))
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Microphone permission was denied'),
    )
    expect(FakeMediaRecorder.instances).toHaveLength(0)
  })

  it('toasts when getUserMedia is unavailable (insecure context)', async () => {
    const { toast } = await import('sonner')
    Object.defineProperty(navigator, 'mediaDevices', { value: undefined, configurable: true })
    render(<Composer {...baseProps()} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Record voice input' }))
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith(
        'Microphone input is not available in this browser or context',
      ),
    )
  })
})

describe('Composer stop button', () => {
  it('shows Send, not Stop, when not streaming', () => {
    render(<Composer {...baseProps()} streaming={false} onStop={vi.fn()} />)
    expect(screen.getByRole('button', { name: 'Send' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Stop' })).toBeNull()
  })

  it('shows Stop instead of Send while streaming, and fires onStop', () => {
    const onStop = vi.fn()
    render(<Composer {...baseProps()} streaming={true} onStop={onStop} />)
    expect(screen.queryByRole('button', { name: 'Send' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Stop' }))
    expect(onStop).toHaveBeenCalledOnce()
  })

  it('falls back to Send while streaming if onStop is not given', () => {
    render(<Composer {...baseProps()} streaming={true} />)
    expect(screen.getByRole('button', { name: 'Send' })).toBeTruthy()
  })
})

function makeImageFile(name = 'photo.png', type = 'image/png', size = 1024): File {
  const file = new File([new Uint8Array(size)], name, { type })
  return file
}

describe('Composer attachments', () => {
  beforeEach(() => {
    vi.stubGlobal('URL', {
      ...URL,
      createObjectURL: vi.fn(() => `blob:mock-${Math.random()}`),
      revokeObjectURL: vi.fn(),
    })
  })

  it('does not render the paperclip button when onAttachments is omitted', () => {
    render(<Composer {...baseProps()} />)
    expect(screen.queryByRole('button', { name: 'Attach image' })).toBeNull()
  })

  it('renders the paperclip button when onAttachments is given', () => {
    render(<Composer {...baseProps()} onAttachments={vi.fn()} />)
    expect(screen.getByRole('button', { name: 'Attach image' })).toBeTruthy()
  })

  it('uploads a selected file and adds a chip on success', async () => {
    const client = await import('../api/client')
    vi.mocked(client.uploadAttachment).mockResolvedValue({
      id: 'att-1',
      mime: 'image/png',
      size_bytes: 1024,
    })
    const onAttachments = vi.fn()
    const { rerender } = render(
      <Composer {...baseProps()} attachments={[]} onAttachments={onAttachments} />,
    )

    const input = document.querySelector('input[type="file"]') as HTMLInputElement
    const file = makeImageFile()
    fireEvent.change(input, { target: { files: [file] } })

    await waitFor(() => expect(client.uploadAttachment).toHaveBeenCalledWith(file))
    await waitFor(() =>
      expect(onAttachments).toHaveBeenLastCalledWith([
        expect.objectContaining({ id: 'att-1', mime: 'image/png', uploading: false }),
      ]),
    )

    const [[chips]] = onAttachments.mock.calls.slice(-1)
    rerender(<Composer {...baseProps()} attachments={chips} onAttachments={onAttachments} />)
    expect(screen.getByAltText('photo.png')).toBeTruthy()
  })

  it('toasts and skips the upload for an oversize file', async () => {
    const client = await import('../api/client')
    const { toast } = await import('sonner')
    const onAttachments = vi.fn()
    render(<Composer {...baseProps()} onAttachments={onAttachments} />)

    const input = document.querySelector('input[type="file"]') as HTMLInputElement
    const big = makeImageFile('big.png', 'image/png', 11 * 1024 * 1024)
    fireEvent.change(input, { target: { files: [big] } })

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('big.png: exceeds the 10MB limit'),
    )
    expect(client.uploadAttachment).not.toHaveBeenCalled()
    expect(onAttachments).not.toHaveBeenCalled()
  })

  it('toasts and skips the upload for an unsupported file type', async () => {
    const client = await import('../api/client')
    const { toast } = await import('sonner')
    const onAttachments = vi.fn()
    render(<Composer {...baseProps()} onAttachments={onAttachments} />)

    const input = document.querySelector('input[type="file"]') as HTMLInputElement
    const bogus = makeImageFile('archive.zip', 'application/zip')
    fireEvent.change(input, { target: { files: [bogus] } })

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('archive.zip: unsupported file type'),
    )
    expect(client.uploadAttachment).not.toHaveBeenCalled()
    expect(onAttachments).not.toHaveBeenCalled()
  })

  it('uploads a selected .txt file and adds a file chip on success', async () => {
    const client = await import('../api/client')
    vi.mocked(client.uploadAttachment).mockResolvedValue({
      id: 'att-txt',
      mime: 'text/plain',
      size_bytes: 20,
    })
    const onAttachments = vi.fn()
    const { rerender } = render(
      <Composer {...baseProps()} attachments={[]} onAttachments={onAttachments} />,
    )

    const input = document.querySelector('input[type="file"]') as HTMLInputElement
    const txt = makeImageFile('notes.txt', 'text/plain')
    fireEvent.change(input, { target: { files: [txt] } })

    await waitFor(() => expect(client.uploadAttachment).toHaveBeenCalledWith(txt))
    await waitFor(() =>
      expect(onAttachments).toHaveBeenLastCalledWith([
        expect.objectContaining({ id: 'att-txt', mime: 'text/plain', uploading: false }),
      ]),
    )

    const [[chips]] = onAttachments.mock.calls.slice(-1)
    rerender(<Composer {...baseProps()} attachments={chips} onAttachments={onAttachments} />)
    expect(screen.getByText('notes.txt')).toBeTruthy()
  })

  it('accepts a .md file with an empty reported type, by extension', async () => {
    const client = await import('../api/client')
    vi.mocked(client.uploadAttachment).mockResolvedValue({
      id: 'att-md',
      mime: 'text/plain',
      size_bytes: 20,
    })
    const onAttachments = vi.fn()
    render(<Composer {...baseProps()} attachments={[]} onAttachments={onAttachments} />)

    const input = document.querySelector('input[type="file"]') as HTMLInputElement
    const md = makeImageFile('README.md', '')
    fireEvent.change(input, { target: { files: [md] } })

    await waitFor(() => expect(client.uploadAttachment).toHaveBeenCalledWith(md))
  })

  it('uploads a selected PDF and adds a file chip on success', async () => {
    const client = await import('../api/client')
    vi.mocked(client.uploadAttachment).mockResolvedValue({
      id: 'att-pdf',
      mime: 'application/pdf',
      size_bytes: 2048,
    })
    const onAttachments = vi.fn()
    const { rerender } = render(
      <Composer {...baseProps()} attachments={[]} onAttachments={onAttachments} />,
    )

    const input = document.querySelector('input[type="file"]') as HTMLInputElement
    const pdf = makeImageFile('doc.pdf', 'application/pdf')
    fireEvent.change(input, { target: { files: [pdf] } })

    await waitFor(() => expect(client.uploadAttachment).toHaveBeenCalledWith(pdf))
    await waitFor(() =>
      expect(onAttachments).toHaveBeenLastCalledWith([
        expect.objectContaining({ id: 'att-pdf', mime: 'application/pdf', uploading: false }),
      ]),
    )

    const [[chips]] = onAttachments.mock.calls.slice(-1)
    rerender(<Composer {...baseProps()} attachments={chips} onAttachments={onAttachments} />)
    expect(screen.getByText('doc.pdf')).toBeTruthy()
    expect(screen.queryByAltText('doc.pdf')).toBeNull()
  })

  it('removes a chip and revokes its object URL', () => {
    const onAttachments = vi.fn()
    const attachments: PendingAttachment[] = [
      { id: 'att-1', mime: 'image/png', previewUrl: 'blob:existing', name: 'a.png' },
    ]
    render(<Composer {...baseProps()} attachments={attachments} onAttachments={onAttachments} />)

    fireEvent.click(screen.getByRole('button', { name: 'Remove a.png' }))
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:existing')
    expect(onAttachments).toHaveBeenCalledWith([])
  })

  it('enables send when attachments exist even with an empty draft', () => {
    const attachments: PendingAttachment[] = [
      { id: 'att-1', mime: 'image/png', previewUrl: 'blob:existing', name: 'a.png' },
    ]
    render(
      <Composer
        {...baseProps()}
        draft=""
        attachments={attachments}
        onAttachments={vi.fn()}
      />,
    )
    expect(screen.getByRole('button', { name: 'Send' })).not.toBeDisabled()
  })

  it('keeps send disabled with an empty draft and no attachments', () => {
    render(<Composer {...baseProps()} draft="" onAttachments={vi.fn()} />)
    expect(screen.getByRole('button', { name: 'Send' })).toBeDisabled()
  })

  it('uploads a selected video file and adds a document chip on success', async () => {
    const client = await import('../api/client')
    vi.mocked(client.uploadAttachment).mockResolvedValue({
      id: 'att-vid',
      mime: 'video/mp4',
      size_bytes: 2048,
    })
    const onAttachments = vi.fn()
    const { rerender } = render(
      <Composer {...baseProps()} attachments={[]} onAttachments={onAttachments} />,
    )

    const input = document.querySelector('input[type="file"]') as HTMLInputElement
    const video = makeImageFile('clip.mp4', 'video/mp4', 2048)
    fireEvent.change(input, { target: { files: [video] } })

    await waitFor(() => expect(client.uploadAttachment).toHaveBeenCalledWith(video))
    await waitFor(() =>
      expect(onAttachments).toHaveBeenLastCalledWith([
        expect.objectContaining({ id: 'att-vid', mime: 'video/mp4', uploading: false }),
      ]),
    )

    const [[chips]] = onAttachments.mock.calls.slice(-1)
    rerender(<Composer {...baseProps()} attachments={chips} onAttachments={onAttachments} />)
    expect(screen.getByText('clip.mp4')).toBeTruthy()
  })

  it('uploads a selected audio file and adds a document chip on success', async () => {
    const client = await import('../api/client')
    vi.mocked(client.uploadAttachment).mockResolvedValue({
      id: 'att-aud',
      mime: 'audio/mpeg',
      size_bytes: 512,
    })
    const onAttachments = vi.fn()
    const { rerender } = render(
      <Composer {...baseProps()} attachments={[]} onAttachments={onAttachments} />,
    )

    const input = document.querySelector('input[type="file"]') as HTMLInputElement
    const audio = makeImageFile('note.mp3', 'audio/mpeg', 512)
    fireEvent.change(input, { target: { files: [audio] } })

    await waitFor(() => expect(client.uploadAttachment).toHaveBeenCalledWith(audio))
    await waitFor(() =>
      expect(onAttachments).toHaveBeenLastCalledWith([
        expect.objectContaining({ id: 'att-aud', mime: 'audio/mpeg', uploading: false }),
      ]),
    )

    const [[chips]] = onAttachments.mock.calls.slice(-1)
    rerender(<Composer {...baseProps()} attachments={chips} onAttachments={onAttachments} />)
    expect(screen.getByText('note.mp3')).toBeTruthy()
  })

  it('allows a video file under the 100MB cap but rejects one over it', async () => {
    const client = await import('../api/client')
    const { toast } = await import('sonner')
    const onAttachments = vi.fn()
    render(<Composer {...baseProps()} onAttachments={onAttachments} />)

    const input = document.querySelector('input[type="file"]') as HTMLInputElement
    const big = makeImageFile('huge.mp4', 'video/mp4', 101 * 1024 * 1024)
    fireEvent.change(input, { target: { files: [big] } })

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('huge.mp4: exceeds the 100MB limit'),
    )
    expect(client.uploadAttachment).not.toHaveBeenCalled()
    expect(onAttachments).not.toHaveBeenCalled()
  })

  it('rejects an audio file over the 25MB cap', async () => {
    const client = await import('../api/client')
    const { toast } = await import('sonner')
    const onAttachments = vi.fn()
    render(<Composer {...baseProps()} onAttachments={onAttachments} />)

    const input = document.querySelector('input[type="file"]') as HTMLInputElement
    const big = makeImageFile('huge.mp3', 'audio/mpeg', 26 * 1024 * 1024)
    fireEvent.change(input, { target: { files: [big] } })

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('huge.mp3: exceeds the 25MB limit'),
    )
    expect(client.uploadAttachment).not.toHaveBeenCalled()
    expect(onAttachments).not.toHaveBeenCalled()
  })

  it('uploads a pasted image from the clipboard', async () => {
    const client = await import('../api/client')
    vi.mocked(client.uploadAttachment).mockResolvedValue({
      id: 'att-2',
      mime: 'image/png',
      size_bytes: 1024,
    })
    const onAttachments = vi.fn()
    render(<Composer {...baseProps()} onAttachments={onAttachments} />)

    const file = makeImageFile('pasted.png')
    const clipboardData = {
      items: [{ kind: 'file', type: 'image/png', getAsFile: () => file }],
    }
    fireEvent.paste(screen.getByRole('textbox', { name: 'Message' }), { clipboardData })

    await waitFor(() => expect(client.uploadAttachment).toHaveBeenCalledWith(file))
  })
})

// StatefulComposer owns draft/knowledge state itself (Composer is a
// controlled component) so a test can drive real typing/selection and
// see the popup and chips update, closer to how the page actually
// wires it than passing static props would allow.
function StatefulComposer({
  onSend,
  onKnowledgeSpy,
}: {
  onSend: () => void
  onKnowledgeSpy?: (next: string[]) => void
}) {
  const [draft, setDraft] = useState('')
  const [knowledge, setKnowledge] = useState<string[]>([])
  return (
    <Composer
      {...baseProps()}
      draft={draft}
      onDraft={setDraft}
      onSend={onSend}
      knowledge={knowledge}
      onKnowledge={(next) => {
        setKnowledge(next)
        onKnowledgeSpy?.(next)
      }}
    />
  )
}

describe('Composer knowledge mentions', () => {
  it('does not show a popup when onKnowledge is omitted', async () => {
    render(<Composer {...baseProps()} draft="#obs" />)
    const input = screen.getByRole('textbox', { name: 'Message' })
    fireEvent.change(input, { target: { value: '#obs' } })
    await new Promise((r) => setTimeout(r, 0))
    expect(screen.queryByText('#observability')).toBeNull()
  })

  it('shows a filtered popup, selects on Enter, strips the token, and stops intercepting Enter', async () => {
    const client = await import('../api/client')
    vi.mocked(client.listKbCollections).mockResolvedValue([
      { id: '1', name: 'observability', description: '', doc_count: 0, chunk_count: 0, failed_count: 0, created_at: '', updated_at: '' },
      { id: '2', name: 'billing', description: '', doc_count: 0, chunk_count: 0, failed_count: 0, created_at: '', updated_at: '' },
    ])
    const onSend = vi.fn()
    render(<StatefulComposer onSend={onSend} />)
    const input = screen.getByRole('textbox', { name: 'Message' }) as HTMLTextAreaElement

    fireEvent.change(input, { target: { value: '#obs' } })

    await screen.findByText('#observability')
    expect(screen.queryByText('#billing')).toBeNull()

    fireEvent.keyDown(input, { key: 'Enter' })
    expect(onSend).not.toHaveBeenCalled()
    expect(input.value).toBe('')
    await screen.findByText('#observability') // now rendered as a chip, not a popup row
    expect(screen.queryByRole('button', { name: /^#observability$/ })).toBeNull()

    // Popup is closed and the token is gone: Enter now sends normally.
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(onSend).toHaveBeenCalledOnce()
  })

  it('closes the popup on Escape and lets Enter send', async () => {
    const client = await import('../api/client')
    vi.mocked(client.listKbCollections).mockResolvedValue([
      { id: '1', name: 'observability', description: '', doc_count: 0, chunk_count: 0, failed_count: 0, created_at: '', updated_at: '' },
    ])
    const onSend = vi.fn()
    render(<StatefulComposer onSend={onSend} />)
    const input = screen.getByRole('textbox', { name: 'Message' }) as HTMLTextAreaElement

    fireEvent.change(input, { target: { value: '#obs' } })
    await screen.findByText('#observability')

    fireEvent.keyDown(input, { key: 'Escape' })
    expect(screen.queryByRole('button', { name: /^#observability$/ })).toBeNull()

    fireEvent.keyDown(input, { key: 'Enter' })
    expect(onSend).toHaveBeenCalledOnce()
  })

  it('removes a chip via its remove button without touching the others', () => {
    const onKnowledge = vi.fn()
    render(
      <Composer
        {...baseProps()}
        draft=""
        knowledge={['observability', 'billing']}
        onKnowledge={onKnowledge}
      />,
    )
    fireEvent.click(screen.getByRole('button', { name: 'Remove observability knowledge' }))
    expect(onKnowledge).toHaveBeenCalledWith(['billing'])
  })

  it('retries the collections fetch on the next # after a transient failure', async () => {
    const client = await import('../api/client')
    vi.mocked(client.listKbCollections)
      .mockRejectedValueOnce(new Error('network error'))
      .mockResolvedValueOnce([
        { id: '1', name: 'observability', description: '', doc_count: 0, chunk_count: 0, failed_count: 0, created_at: '', updated_at: '' },
      ])
    const onSend = vi.fn()
    render(<StatefulComposer onSend={onSend} />)
    const input = screen.getByRole('textbox', { name: 'Message' }) as HTMLTextAreaElement

    fireEvent.change(input, { target: { value: '#obs' } })
    await waitFor(() => expect(client.listKbCollections).toHaveBeenCalledTimes(1))
    expect(screen.queryByText('#observability')).toBeNull()

    fireEvent.change(input, { target: { value: '#ob' } })
    fireEvent.change(input, { target: { value: '#obs' } })
    await waitFor(() => expect(client.listKbCollections).toHaveBeenCalledTimes(2))
    await screen.findByText('#observability')
  })

  it('cycles the highlighted option with ArrowDown/ArrowUp, wrapping at both ends', async () => {
    const client = await import('../api/client')
    vi.mocked(client.listKbCollections).mockResolvedValue([
      { id: '1', name: 'observability', description: '', doc_count: 0, chunk_count: 0, failed_count: 0, created_at: '', updated_at: '' },
      { id: '2', name: 'billing', description: '', doc_count: 0, chunk_count: 0, failed_count: 0, created_at: '', updated_at: '' },
    ])
    const onSend = vi.fn()
    render(<StatefulComposer onSend={onSend} />)
    const input = screen.getByRole('textbox', { name: 'Message' }) as HTMLTextAreaElement

    fireEvent.change(input, { target: { value: '#' } })
    await screen.findByText('#observability')

    const highlighted = () =>
      screen.getByText('#observability').className.includes('bg-zinc-100')
        ? '#observability'
        : '#billing'

    expect(highlighted()).toBe('#observability')

    fireEvent.keyDown(input, { key: 'ArrowDown' })
    expect(highlighted()).toBe('#billing')

    fireEvent.keyDown(input, { key: 'ArrowDown' })
    expect(highlighted()).toBe('#observability') // wraps past the end

    fireEvent.keyDown(input, { key: 'ArrowUp' })
    expect(highlighted()).toBe('#billing') // wraps back past the start
  })

  it('selects the clicked popup option, adding a chip and stripping the token', async () => {
    const client = await import('../api/client')
    vi.mocked(client.listKbCollections).mockResolvedValue([
      { id: '1', name: 'observability', description: '', doc_count: 0, chunk_count: 0, failed_count: 0, created_at: '', updated_at: '' },
      { id: '2', name: 'billing', description: '', doc_count: 0, chunk_count: 0, failed_count: 0, created_at: '', updated_at: '' },
    ])
    const onSend = vi.fn()
    render(<StatefulComposer onSend={onSend} />)
    const input = screen.getByRole('textbox', { name: 'Message' }) as HTMLTextAreaElement

    fireEvent.change(input, { target: { value: '#' } })
    await screen.findByText('#billing')

    fireEvent.click(screen.getByText('#billing'))

    expect(input.value).toBe('')
    expect(screen.queryByText('#observability')).toBeNull() // popup closed
    expect(screen.getByRole('button', { name: 'Remove billing knowledge' })).toBeTruthy()
  })

  it('selects the highlighted option on Tab, same as Enter', async () => {
    const client = await import('../api/client')
    vi.mocked(client.listKbCollections).mockResolvedValue([
      { id: '1', name: 'observability', description: '', doc_count: 0, chunk_count: 0, failed_count: 0, created_at: '', updated_at: '' },
    ])
    const onSend = vi.fn()
    render(<StatefulComposer onSend={onSend} />)
    const input = screen.getByRole('textbox', { name: 'Message' }) as HTMLTextAreaElement

    fireEvent.change(input, { target: { value: '#obs' } })
    await screen.findByText('#observability')

    fireEvent.keyDown(input, { key: 'Tab' })

    expect(input.value).toBe('')
    expect(screen.getByRole('button', { name: 'Remove observability knowledge' })).toBeTruthy()
  })
})

describe('Composer agent-bound knowledge chips', () => {
  it('renders agent-bound collections as muted, non-removable chips', () => {
    render(
      <Composer
        {...baseProps()}
        draft=""
        knowledge={[]}
        onKnowledge={vi.fn()}
        agentKnowledge={['runbooks']}
      />,
    )
    expect(screen.getByText('#runbooks')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Remove runbooks knowledge' })).toBeNull()
  })

  it('excludes agent-bound names from the mention popup', async () => {
    const client = await import('../api/client')
    vi.mocked(client.listKbCollections).mockResolvedValue([
      { id: '1', name: 'observability', description: '', doc_count: 0, chunk_count: 0, failed_count: 0, created_at: '', updated_at: '' },
      { id: '2', name: 'runbooks', description: '', doc_count: 0, chunk_count: 0, failed_count: 0, created_at: '', updated_at: '' },
    ])
    function StatefulWithAgentKnowledge() {
      const [draft, setDraft] = useState('')
      const [knowledge, setKnowledge] = useState<string[]>([])
      return (
        <Composer
          {...baseProps()}
          draft={draft}
          onDraft={setDraft}
          knowledge={knowledge}
          onKnowledge={setKnowledge}
          agentKnowledge={['runbooks']}
        />
      )
    }
    render(<StatefulWithAgentKnowledge />)
    const input = screen.getByRole('textbox', { name: 'Message' })

    fireEvent.change(input, { target: { value: '#r' } })
    await waitFor(() => expect(client.listKbCollections).toHaveBeenCalled())
    // 'runbooks' matches the query but is agent-bound, so it's excluded.
    expect(screen.queryByText('#runbooks', { selector: 'button' })).toBeNull()
  })

  it('hides the duplicate muted chip when a name is both agent-bound and session-pinned', () => {
    render(
      <Composer
        {...baseProps()}
        draft=""
        knowledge={['runbooks']}
        onKnowledge={vi.fn()}
        agentKnowledge={['runbooks']}
      />,
    )
    // Only the removable violet chip renders, not a second muted one.
    expect(screen.getByRole('button', { name: 'Remove runbooks knowledge' })).toBeTruthy()
    expect(screen.getAllByText('#runbooks')).toHaveLength(1)
  })
})

// StatefulComposerWithReferences owns draft/references state itself,
// same rationale as StatefulComposer above: a test drives real typing
// and sees the popup/chips update.
function StatefulComposerWithReferences({ onSend = vi.fn() }: { onSend?: () => void }) {
  const [draft, setDraft] = useState('')
  const [references, setReferences] = useState<Reference[]>([])
  return (
    <Composer
      {...baseProps()}
      draft={draft}
      onDraft={setDraft}
      onSend={onSend}
      references={references}
      onReferences={setReferences}
    />
  )
}

describe('Composer reference mentions', () => {
  it('does not show a popup when onReferences is omitted', async () => {
    render(<Composer {...baseProps()} draft="#fix" />)
    const input = screen.getByRole('textbox', { name: 'Message' })
    fireEvent.change(input, { target: { value: '#fix' } })
    await new Promise((r) => setTimeout(r, 300))
    expect(screen.queryByText('Missions')).toBeNull()
  })

  it('debounces the search and fires it once per pause in typing', async () => {
    const client = await import('../api/client')
    render(<StatefulComposerWithReferences />)
    const input = screen.getByRole('textbox', { name: 'Message' })

    fireEvent.change(input, { target: { value: '#f' } })
    fireEvent.change(input, { target: { value: '#fi' } })
    fireEvent.change(input, { target: { value: '#fix' } })

    await new Promise((r) => setTimeout(r, 100))
    expect(client.listMissions).not.toHaveBeenCalled()

    await waitFor(() => expect(client.listMissions).toHaveBeenCalledTimes(1))
    expect(client.listMissions).toHaveBeenCalledWith({ query: 'fix', limit: 8 })
  })

  it('shows a grouped popup, picks an entry, and strips the token', async () => {
    const client = await import('../api/client')
    vi.mocked(client.listMissions).mockResolvedValue([
      { id: 'mi1', name: 'Fix the bug', goal: 'Fix the bug' } as never,
    ])
    render(<StatefulComposerWithReferences />)
    const input = screen.getByRole('textbox', { name: 'Message' }) as HTMLTextAreaElement

    fireEvent.change(input, { target: { value: 'please #fix' } })

    await screen.findByText('Missions')
    fireEvent.click(screen.getByText('Fix the bug'))

    expect(input.value).toBe('please ')
    expect(screen.queryByText('Missions')).toBeNull()
    expect(screen.getByRole('button', { name: 'Remove Fix the bug reference' })).toBeTruthy()
  })

  it('removes a reference chip via its remove button', () => {
    const onReferences = vi.fn()
    render(
      <Composer
        {...baseProps()}
        draft=""
        references={[{ kind: 'mission', id: 'mi1', name: 'Fix the bug' }]}
        onReferences={onReferences}
      />,
    )
    fireEvent.click(screen.getByRole('button', { name: 'Remove Fix the bug reference' }))
    expect(onReferences).toHaveBeenCalledWith([])
  })

  it('sends payload-shaped references (kind/id only rides the wire, name is chip-only)', () => {
    const references: Reference[] = [{ kind: 'kb_doc', id: 'd1', name: 'Runbook' }]
    render(<Composer {...baseProps()} draft="" references={references} onReferences={vi.fn()} />)
    expect(screen.getByText(/Runbook/)).toBeTruthy()
  })

  it('does not add a reference past the cap of 8', async () => {
    const client = await import('../api/client')
    vi.mocked(client.listMissions).mockResolvedValue([
      { id: 'mi9', name: 'Ninth mission', goal: 'Ninth mission' } as never,
    ])
    const eight: Reference[] = Array.from({ length: 8 }, (_, i) => ({
      kind: 'mission' as const,
      id: `m${i}`,
      name: `Mission ${i}`,
    }))
    const onReferences = vi.fn()
    function StatefulAtCap() {
      const [draft, setDraft] = useState('')
      return (
        <Composer
          {...baseProps()}
          draft={draft}
          onDraft={setDraft}
          references={eight}
          onReferences={onReferences}
        />
      )
    }
    render(<StatefulAtCap />)
    const input = screen.getByRole('textbox', { name: 'Message' })

    fireEvent.change(input, { target: { value: '#ninth' } })
    await new Promise((r) => setTimeout(r, 300))

    expect(screen.queryByText('Missions')).toBeNull()
    expect(onReferences).not.toHaveBeenCalled()
  })

  it('cycles the highlighted option with ArrowDown/ArrowUp across groups', async () => {
    const client = await import('../api/client')
    vi.mocked(client.listMissions).mockResolvedValue([
      { id: 'mi1', name: 'Fix the bug', goal: 'Fix the bug' } as never,
    ])
    vi.mocked(client.listSessions).mockResolvedValue([
      { id: 'se1', title: 'Old chat', archived: false, created_at: '', updated_at: '' },
    ])
    render(<StatefulComposerWithReferences />)
    const input = screen.getByRole('textbox', { name: 'Message' })

    fireEvent.change(input, { target: { value: '#' } })
    await screen.findByText('Fix the bug')
    await screen.findByText('Old chat')

    const highlighted = () =>
      screen.getByText('Fix the bug').className.includes('bg-zinc-100') ? 'mission' : 'chat'
    expect(highlighted()).toBe('mission')

    fireEvent.keyDown(input, { key: 'ArrowDown' })
    expect(highlighted()).toBe('chat')
  })
})
