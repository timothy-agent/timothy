import { createContext, useContext, useEffect, useState, type ReactNode } from 'react'
import { getAuthStatus, getToken, logout, subscribeAuth, type AuthStatus, type AuthUser } from '../api/client'

interface AuthContextValue {
  user: AuthUser | null
  authenticated: boolean
  setupRequired: boolean
  loading: boolean
  refreshAuth: () => Promise<void>
  handleLogout: () => Promise<void>
}

const AuthContext = createContext<AuthContextValue | null>(null)

export function AuthProvider({ children }: { children: ReactNode }) {
  const [authStatus, setAuthStatus] = useState<AuthStatus>(() => ({
    setup_required: false,
    authenticated: Boolean(getToken()),
    user: null,
  }))
  const [loading, setLoading] = useState(false)

  const refreshAuth = async () => {
    try {
      const status = await getAuthStatus()
      setAuthStatus(status)
    } catch {
      // If fetching status fails, keep token-based auth if token exists
      if (!getToken()) {
        setAuthStatus((prev) => ({ ...prev, authenticated: false }))
      }
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    refreshAuth()
  }, [])

  useEffect(() => {
    return subscribeAuth((notification) => {
      if (notification === 'setup_required' && !getToken()) {
        setAuthStatus((prev) => ({ ...prev, setup_required: true, authenticated: false }))
      } else if (notification === 'unauthorized' && !getToken()) {
        setAuthStatus((prev) => ({ ...prev, authenticated: false }))
      }
    })
  }, [])

  const handleLogout = async () => {
    try {
      await logout()
    } finally {
      setAuthStatus({
        setup_required: false,
        authenticated: false,
        user: null,
      })
    }
  }

  return (
    <AuthContext.Provider
      value={{
        user: authStatus.user,
        authenticated: authStatus.authenticated,
        setupRequired: authStatus.setup_required,
        loading,
        refreshAuth,
        handleLogout,
      }}
    >
      {children}
    </AuthContext.Provider>
  )
}

const defaultAuthValue: AuthContextValue = {
  user: null,
  authenticated: false,
  setupRequired: false,
  loading: false,
  refreshAuth: async () => {},
  handleLogout: async () => {},
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext)
  return ctx ?? defaultAuthValue
}
