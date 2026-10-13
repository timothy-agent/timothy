import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { setupAdmin } from '../api/client'
import { AuthProvider } from '../lib/auth'
import { Setup } from './Setup'

const navigate = vi.fn()
vi.mock('react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('react-router')>()),
  useNavigate: () => navigate,
}))

vi.mock('../api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../api/client')>()),
  setupAdmin: vi.fn(),
  getAuthStatus: vi.fn().mockResolvedValue({ setup_required: true, authenticated: false, user: null }),
}))

function renderSetup() {
  return render(
    <MemoryRouter initialEntries={['/setup']}>
      <AuthProvider>
        <Setup />
      </AuthProvider>
    </MemoryRouter>,
  )
}

describe('Setup page', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('renders initial setup form fields', () => {
    renderSetup()

    expect(screen.getByRole('heading', { name: /initial setup/i })).toBeInTheDocument()
    expect(screen.getByLabelText(/username/i)).toHaveValue('admin')
    expect(screen.getByLabelText(/email address/i)).toBeInTheDocument()
    expect(screen.getByLabelText(/^password/i)).toBeInTheDocument()
    expect(screen.getByLabelText(/confirm password/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /complete setup & sign in/i })).toBeInTheDocument()
  })

  it('validates password mismatch on submit', async () => {
    renderSetup()

    fireEvent.change(screen.getByLabelText(/email address/i), { target: { value: 'admin@example.com' } })
    fireEvent.change(screen.getByLabelText(/^password/i), { target: { value: 'password123' } })
    fireEvent.change(screen.getByLabelText(/confirm password/i), { target: { value: 'password999' } })
    fireEvent.click(screen.getByRole('button', { name: /complete setup & sign in/i }))

    expect(await screen.findByText(/passwords do not match/i)).toBeInTheDocument()
    expect(setupAdmin).not.toHaveBeenCalled()
  })

  it('submits valid form to setupAdmin and navigates to root', async () => {
    vi.mocked(setupAdmin).mockResolvedValueOnce(undefined)
    renderSetup()

    fireEvent.change(screen.getByLabelText(/email address/i), { target: { value: 'admin@example.com' } })
    fireEvent.change(screen.getByLabelText(/^password/i), { target: { value: 'StrongPassword123!' } })
    fireEvent.change(screen.getByLabelText(/confirm password/i), { target: { value: 'StrongPassword123!' } })
    fireEvent.click(screen.getByRole('button', { name: /complete setup & sign in/i }))

    await waitFor(() => {
      expect(setupAdmin).toHaveBeenCalledWith('admin', 'admin@example.com', 'StrongPassword123!')
      expect(navigate).toHaveBeenCalledWith('/', { replace: true })
    })
  })

  it('displays API error when setupAdmin fails', async () => {
    vi.mocked(setupAdmin).mockRejectedValueOnce(new Error('initial admin account already exists'))
    renderSetup()

    fireEvent.change(screen.getByLabelText(/email address/i), { target: { value: 'admin@example.com' } })
    fireEvent.change(screen.getByLabelText(/^password/i), { target: { value: 'StrongPassword123!' } })
    fireEvent.change(screen.getByLabelText(/confirm password/i), { target: { value: 'StrongPassword123!' } })
    fireEvent.click(screen.getByRole('button', { name: /complete setup & sign in/i }))

    expect(await screen.findByText(/initial admin account already exists/i)).toBeInTheDocument()
  })
})
