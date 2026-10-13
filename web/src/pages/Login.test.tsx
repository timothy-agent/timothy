import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { login, resetPasswordWithToken } from '../api/client'
import { AuthProvider } from '../lib/auth'
import { Login } from './Login'

const navigate = vi.fn()
vi.mock('react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('react-router')>()),
  useNavigate: () => navigate,
}))

vi.mock('../api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../api/client')>()),
  login: vi.fn(),
  resetPasswordWithToken: vi.fn(),
  getAuthStatus: vi.fn().mockResolvedValue({ setup_required: false, authenticated: false, user: null }),
}))

function renderLogin() {
  return render(
    <MemoryRouter initialEntries={['/login']}>
      <AuthProvider>
        <Login />
      </AuthProvider>
    </MemoryRouter>,
  )
}

describe('Login page', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('renders login form fields', () => {
    renderLogin()

    expect(screen.getByRole('heading', { name: /sign in/i })).toBeInTheDocument()
    expect(screen.getByLabelText(/username or email/i)).toBeInTheDocument()
    expect(screen.getByLabelText(/^password/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /^sign in$/i })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /forgot password\?/i })).toBeInTheDocument()
  })

  it('submits valid credentials and navigates to root', async () => {
    vi.mocked(login).mockResolvedValueOnce(undefined)
    renderLogin()

    fireEvent.change(screen.getByLabelText(/username or email/i), { target: { value: 'alice' } })
    fireEvent.change(screen.getByLabelText(/^password/i), { target: { value: 'Secret123!' } })
    fireEvent.click(screen.getByRole('button', { name: /^sign in$/i }))

    await waitFor(() => {
      expect(login).toHaveBeenCalledWith('alice', 'Secret123!')
      expect(navigate).toHaveBeenCalledWith('/', { replace: true })
    })
  })

  it('displays error when credentials are invalid', async () => {
    vi.mocked(login).mockRejectedValueOnce(new Error('invalid username or password'))
    renderLogin()

    fireEvent.change(screen.getByLabelText(/username or email/i), { target: { value: 'alice' } })
    fireEvent.change(screen.getByLabelText(/^password/i), { target: { value: 'wrongpass' } })
    fireEvent.click(screen.getByRole('button', { name: /^sign in$/i }))

    expect(await screen.findByText(/invalid username or password/i)).toBeInTheDocument()
  })

  it('switches to recovery mode and resets password', async () => {
    vi.mocked(resetPasswordWithToken).mockResolvedValueOnce(undefined)
    renderLogin()

    fireEvent.click(screen.getByRole('button', { name: /forgot password\?/i }))

    expect(screen.getByRole('heading', { name: /reset password/i })).toBeInTheDocument()
    expect(screen.getByText(/docker compose exec brain \/app -reset-password/i)).toBeInTheDocument()

    fireEvent.change(screen.getByLabelText(/recovery token/i), { target: { value: 'tok-12345' } })
    fireEvent.change(screen.getByLabelText(/^new password/i), { target: { value: 'NewSecret123!' } })
    fireEvent.change(screen.getByLabelText(/confirm new password/i), { target: { value: 'NewSecret123!' } })
    fireEvent.click(screen.getByRole('button', { name: /^reset password$/i }))

    await waitFor(() => {
      expect(resetPasswordWithToken).toHaveBeenCalledWith('tok-12345', 'NewSecret123!')
      expect(screen.getByText(/password reset successfully/i)).toBeInTheDocument()
      expect(screen.getByRole('heading', { name: /sign in/i })).toBeInTheDocument()
    })
  })
})
