import { useState } from 'react'
import { useNavigate } from 'react-router'
import { login, resetPasswordWithToken } from '../api/client'
import { useAuth } from '../lib/auth'
import { BrandMark } from '../components/BrandMark'
import { Button } from '../components/ui/button'
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from '../components/ui/card'
import { Input } from '../components/ui/input'
import { Label } from '../components/ui/label'

export function Login() {
  const [mode, setMode] = useState<'login' | 'recovery'>('login')
  const [identifier, setIdentifier] = useState('')
  const [password, setPassword] = useState('')

  // Recovery state
  const [token, setToken] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [confirmPassword, setConfirmPassword] = useState('')

  const [error, setError] = useState<string | null>(null)
  const [message, setMessage] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)

  const { refreshAuth } = useAuth()
  const navigate = useNavigate()

  const handleLogin = async (e: React.FormEvent) => {
    e.preventDefault()
    setError(null)
    setMessage(null)

    if (!identifier.trim()) {
      setError('Username or email is required')
      return
    }
    if (!password) {
      setError('Password is required')
      return
    }

    setLoading(true)
    try {
      await login(identifier.trim(), password)
      await refreshAuth()
      navigate('/', { replace: true })
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : 'Login failed')
    } finally {
      setLoading(false)
    }
  }

  const handleRecovery = async (e: React.FormEvent) => {
    e.preventDefault()
    setError(null)
    setMessage(null)

    if (!token.trim()) {
      setError('Recovery token is required')
      return
    }
    if (newPassword.length < 8) {
      setError('New password must be at least 8 characters long')
      return
    }
    if (newPassword !== confirmPassword) {
      setError('Passwords do not match')
      return
    }

    setLoading(true)
    try {
      await resetPasswordWithToken(token.trim(), newPassword)
      setMessage('Password reset successfully. You can now sign in with your new password.')
      setMode('login')
      setPassword('')
      setToken('')
      setNewPassword('')
      setConfirmPassword('')
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : 'Password reset failed')
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="flex min-h-dvh flex-col items-center justify-center bg-background px-4 py-12">
      <div className="mx-auto flex w-full max-w-md flex-col items-center space-y-6">
        <div className="flex items-center space-x-3">
          <BrandMark className="size-8" />
          <span className="glow-text font-mono text-xl font-bold tracking-[0.2em] text-brand-text uppercase">
            Timothy
          </span>
        </div>

        <Card className="w-full shadow-lg">
          {mode === 'login' ? (
            <form onSubmit={handleLogin}>
              <CardHeader className="space-y-1">
                <CardTitle>
                  <h1 className="text-xl font-semibold">Sign In</h1>
                </CardTitle>
                <CardDescription>
                  Enter your credentials to access your Timothy workspace.
                </CardDescription>
              </CardHeader>

              <CardContent className="space-y-4">
                {message && (
                  <div className="rounded-md bg-primary/15 p-3 text-sm text-primary">
                    {message}
                  </div>
                )}
                {error && (
                  <div className="rounded-md bg-destructive/15 p-3 text-sm text-destructive">
                    {error}
                  </div>
                )}

                <div className="space-y-2">
                  <Label htmlFor="identifier">Username or Email</Label>
                  <Input
                    id="identifier"
                    type="text"
                    value={identifier}
                    onChange={(e) => setIdentifier(e.target.value)}
                    placeholder="admin or admin@example.com"
                    required
                    autoFocus
                  />
                </div>

                <div className="space-y-2">
                  <div className="flex items-center justify-between">
                    <Label htmlFor="password">Password</Label>
                    <button
                      type="button"
                      onClick={() => {
                        setMode('recovery')
                        setError(null)
                        setMessage(null)
                      }}
                      className="text-xs text-muted-foreground underline underline-offset-2 hover:text-foreground"
                    >
                      Forgot password?
                    </button>
                  </div>
                  <Input
                    id="password"
                    type="password"
                    value={password}
                    onChange={(e) => setPassword(e.target.value)}
                    placeholder="••••••••"
                    required
                  />
                </div>
              </CardContent>

              <CardFooter>
                <Button type="submit" className="w-full" disabled={loading}>
                  {loading ? 'Signing in…' : 'Sign In'}
                </Button>
              </CardFooter>
            </form>
          ) : (
            <form onSubmit={handleRecovery}>
              <CardHeader className="space-y-1">
                <CardTitle>
                  <h1 className="text-xl font-semibold">Reset Password</h1>
                </CardTitle>
                <CardDescription>
                  Use a CLI break-glass recovery token to reset your password.
                </CardDescription>
              </CardHeader>

              <CardContent className="space-y-4">
                <div className="rounded-md border border-border bg-muted/50 p-3 text-xs text-muted-foreground">
                  Run on your server:
                  <code className="mt-1 block rounded bg-background p-1.5 font-mono text-[11px] text-foreground">
                    docker compose exec brain /app -reset-password &lt;email&gt;
                  </code>
                </div>

                {error && (
                  <div className="rounded-md bg-destructive/15 p-3 text-sm text-destructive">
                    {error}
                  </div>
                )}

                <div className="space-y-2">
                  <Label htmlFor="token">Recovery Token</Label>
                  <Input
                    id="token"
                    type="text"
                    value={token}
                    onChange={(e) => setToken(e.target.value)}
                    placeholder="64-character token"
                    required
                    autoFocus
                  />
                </div>

                <div className="space-y-2">
                  <Label htmlFor="newPassword">New Password</Label>
                  <Input
                    id="newPassword"
                    type="password"
                    value={newPassword}
                    onChange={(e) => setNewPassword(e.target.value)}
                    placeholder="••••••••"
                    required
                  />
                </div>

                <div className="space-y-2">
                  <Label htmlFor="confirmPassword">Confirm New Password</Label>
                  <Input
                    id="confirmPassword"
                    type="password"
                    value={confirmPassword}
                    onChange={(e) => setConfirmPassword(e.target.value)}
                    placeholder="••••••••"
                    required
                  />
                </div>
              </CardContent>

              <CardFooter className="flex flex-col space-y-2">
                <Button type="submit" className="w-full" disabled={loading}>
                  {loading ? 'Resetting password…' : 'Reset Password'}
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  className="w-full"
                  onClick={() => {
                    setMode('login')
                    setError(null)
                  }}
                >
                  Back to Sign In
                </Button>
              </CardFooter>
            </form>
          )}
        </Card>
      </div>
    </div>
  )
}
