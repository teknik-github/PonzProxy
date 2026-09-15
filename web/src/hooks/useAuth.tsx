import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from 'react'
import { api, ApiError, session, setUnauthorizedHandler } from '../api/client'
import type { User } from '../api/types'

interface AuthState {
  user: User | null
  /** True until the stored token has been checked against the server, so the
   *  app does not flash the sign-in screen on every reload. */
  checking: boolean
  signIn: (username: string, password: string) => Promise<void>
  signOut: () => void
}

const AuthContext = createContext<AuthState | null>(null)

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null)
  const [checking, setChecking] = useState(true)

  const signOut = useCallback(() => {
    session.clear()
    setUser(null)
  }, [])

  // Any 401 from anywhere returns the app to the sign-in screen.
  useEffect(() => {
    setUnauthorizedHandler(() => setUser(null))
    return () => setUnauthorizedHandler(null)
  }, [])

  useEffect(() => {
    if (!session.get()) {
      setChecking(false)
      return
    }
    let cancelled = false
    api
      .me()
      .then((me) => {
        if (!cancelled) setUser(me)
      })
      .catch((err: unknown) => {
        // An expired token is ordinary; anything else is worth surfacing in
        // the console for whoever is debugging a deployment.
        if (!(err instanceof ApiError) || !err.isUnauthorized) {
          console.error('could not restore the session', err)
        }
      })
      .finally(() => {
        if (!cancelled) setChecking(false)
      })
    return () => {
      cancelled = true
    }
  }, [])

  const signIn = useCallback(async (username: string, password: string) => {
    const result = await api.login(username, password)
    session.set(result.token)
    setUser(result.user)
  }, [])

  const value = useMemo<AuthState>(
    () => ({ user, checking, signIn, signOut }),
    [user, checking, signIn, signOut],
  )

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth must be used inside AuthProvider')
  return ctx
}
