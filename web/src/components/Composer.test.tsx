import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { Composer, type PendingAttachment } from './Composer'

vi.mock('../api/client', () => ({
  listAgents: vi.fn().mockResolvedValue([]),
  listRoutes: vi.fn().mockResolvedValue([]),
  transcribe: vi.fn(),
  uploadAttachment: vi.fn(),
  getSettings: vi.fn().mockResolvedValue({ settings: { transcribe_enabled: true }, values: {} }),
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
    const bogus = makeImageFile('notes.txt', 'text/plain')
    fireEvent.change(input, { target: { files: [bogus] } })

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('notes.txt: unsupported file type'),
    )
    expect(client.uploadAttachment).not.toHaveBeenCalled()
    expect(onAttachments).not.toHaveBeenCalled()
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
