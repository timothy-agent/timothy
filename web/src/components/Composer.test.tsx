import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { Composer } from './Composer'

vi.mock('../api/client', () => ({
  listAgents: vi.fn().mockResolvedValue([]),
  listRoutes: vi.fn().mockResolvedValue([]),
  transcribe: vi.fn(),
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
  it('renders a mic button', () => {
    render(<Composer {...baseProps()} />)
    expect(screen.getByRole('button', { name: 'Record voice input' })).toBeTruthy()
  })

  it('records, stops, transcribes, and appends the text to the draft', async () => {
    const client = await import('../api/client')
    vi.mocked(client.transcribe).mockResolvedValue('hello world')
    const onDraft = vi.fn()
    render(<Composer {...baseProps()} onDraft={onDraft} />)

    fireEvent.click(screen.getByRole('button', { name: 'Record voice input' }))
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

    fireEvent.click(screen.getByRole('button', { name: 'Record voice input' }))
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

    fireEvent.click(screen.getByRole('button', { name: 'Record voice input' }))
    await waitFor(() => expect(FakeMediaRecorder.instances).toHaveLength(1))
    fireEvent.click(screen.getByRole('button', { name: 'Stop recording' }))

    await waitFor(() => expect(toast.error).toHaveBeenCalledWith('transcribe failed (502)'))
    expect(onDraft).not.toHaveBeenCalled()
  })

  it('toasts when the microphone permission is denied', async () => {
    const { toast } = await import('sonner')
    getUserMedia.mockRejectedValueOnce(new Error('denied'))
    render(<Composer {...baseProps()} />)

    fireEvent.click(screen.getByRole('button', { name: 'Record voice input' }))
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Microphone permission was denied'),
    )
    expect(FakeMediaRecorder.instances).toHaveLength(0)
  })

  it('toasts when getUserMedia is unavailable (insecure context)', async () => {
    const { toast } = await import('sonner')
    Object.defineProperty(navigator, 'mediaDevices', { value: undefined, configurable: true })
    render(<Composer {...baseProps()} />)

    fireEvent.click(screen.getByRole('button', { name: 'Record voice input' }))
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith(
        'Microphone input is not available in this browser or context',
      ),
    )
  })
})
