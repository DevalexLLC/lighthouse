import { useCallback, useEffect, useState } from 'react'
import { ApiError, apiGet, apiPost, setCsrfToken } from './api'
import type { LoginResponse, User } from './types'
import Login from './views/Login'
import Matrix from './views/Matrix'
import PairDetail from './views/PairDetail'

// Hash routing: '#/' → matrix, '#/pair/{a}/{b}' → pair detail. Login is an
// auth-gate state, not a route, so deep links survive a login round-trip.
type Route = { view: 'matrix' } | { view: 'pair'; a: string; b: string }

function parseHash(hash: string): Route {
  const parts = hash.replace(/^#\/?/, '').split('/')
  if (parts[0] === 'pair' && parts[1] && parts[2]) {
    return { view: 'pair', a: decodeURIComponent(parts[1]), b: decodeURIComponent(parts[2]) }
  }
  return { view: 'matrix' }
}

export default function App() {
  const [booted, setBooted] = useState(false)
  const [user, setUser] = useState<User | null>(null)
  const [route, setRoute] = useState<Route>(() => parseHash(location.hash))

  useEffect(() => {
    const onHash = () => setRoute(parseHash(location.hash))
    window.addEventListener('hashchange', onHash)
    return () => window.removeEventListener('hashchange', onHash)
  }, [])

  // Restore an existing session (and its CSRF token) on boot.
  useEffect(() => {
    apiGet<LoginResponse>('/api/v1/auth/me')
      .then((res) => {
        setCsrfToken(res.csrf_token)
        setUser(res.user)
      })
      .catch(() => setUser(null))
      .finally(() => setBooted(true))
  }, [])

  // Any 401 from a view means the session died server-side: back to login.
  const onAuthError = useCallback((err: unknown) => {
    if (err instanceof ApiError && err.status === 401) setUser(null)
  }, [])

  const logout = useCallback(async () => {
    try {
      await apiPost('/api/v1/auth/logout')
    } catch {
      /* session may already be gone; either way we are logged out */
    }
    setCsrfToken('')
    setUser(null)
  }, [])

  if (!booted) return null
  if (!user) {
    return (
      <Login
        onLogin={(res) => {
          setCsrfToken(res.csrf_token)
          setUser(res.user)
        }}
      />
    )
  }

  return (
    <div className="app">
      <header className="topbar">
        <a className="brand" href="#/">
          Lighthouse
        </a>
        <div className="topbar-right">
          <span className="username">
            {user.username} ({user.role})
          </span>
          <button className="linklike" onClick={logout}>
            Log out
          </button>
        </div>
      </header>
      <main>
        {route.view === 'matrix' ? (
          <Matrix onAuthError={onAuthError} />
        ) : (
          <PairDetail a={route.a} b={route.b} onAuthError={onAuthError} />
        )}
      </main>
    </div>
  )
}
