import { useCallback, useEffect, useState } from 'react'
import { ApiError, apiGet, apiPost, setCsrfToken } from './api'
import type { LoginResponse, User } from './types'
import Agents from './views/Agents'
import Login from './views/Login'
import Matrix from './views/Matrix'
import Outages from './views/Outages'
import PairDetail from './views/PairDetail'
import Paths from './views/Paths'

// Hash routing: '#/' → matrix, '#/pair/{a}/{b}' → pair detail, '#/outages'
// and '#/paths' → the event logs, '#/agents' → fleet health. Login is an
// auth-gate state, not a route, so deep links survive a login round-trip.
type Route =
  | { view: 'matrix' }
  | { view: 'pair'; a: string; b: string }
  | { view: 'outages' }
  | { view: 'paths' }
  | { view: 'agents' }

function parseHash(hash: string): Route {
  const parts = hash.replace(/^#\/?/, '').split('/')
  if (parts[0] === 'pair' && parts[1] && parts[2]) {
    return { view: 'pair', a: decodeURIComponent(parts[1]), b: decodeURIComponent(parts[2]) }
  }
  if (parts[0] === 'outages') return { view: 'outages' }
  if (parts[0] === 'paths') return { view: 'paths' }
  if (parts[0] === 'agents') return { view: 'agents' }
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

  if (!booted) {
    return (
      <div className="boot-state" role="status">
        <img className="logo-mark logo-mark-boot" src="/lighthouse-mark.svg" alt="" />
        Loading Lighthouse…
      </div>
    )
  }
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
          <img className="logo-mark logo-mark-header" src="/lighthouse-mark.svg" alt="" />
          Lighthouse
        </a>
        <nav className="topnav" aria-label="Views">
          <a
            href="#/"
            className={route.view === 'matrix' || route.view === 'pair' ? 'active' : ''}
            aria-current={route.view === 'matrix' || route.view === 'pair' ? 'page' : undefined}
          >
            Sightlines
          </a>
          <a
            href="#/outages"
            className={route.view === 'outages' ? 'active' : ''}
            aria-current={route.view === 'outages' ? 'page' : undefined}
          >
            Outages
          </a>
          <a
            href="#/paths"
            className={route.view === 'paths' ? 'active' : ''}
            aria-current={route.view === 'paths' ? 'page' : undefined}
          >
            Passages
          </a>
          <a
            href="#/agents"
            className={route.view === 'agents' ? 'active' : ''}
            aria-current={route.view === 'agents' ? 'page' : undefined}
          >
            Agents
          </a>
        </nav>
        <div className="topbar-right">
          <span className="username">
            {user.username} <span className="role">· {user.role}</span>
          </span>
          <button className="linklike" onClick={logout}>
            Log out
          </button>
        </div>
      </header>
      <main>
        {route.view === 'matrix' ? (
          <Matrix onAuthError={onAuthError} isAdmin={user.role === 'admin'} />
        ) : route.view === 'pair' ? (
          <PairDetail a={route.a} b={route.b} onAuthError={onAuthError} />
        ) : route.view === 'outages' ? (
          <Outages onAuthError={onAuthError} />
        ) : route.view === 'agents' ? (
          <Agents onAuthError={onAuthError} />
        ) : (
          <Paths onAuthError={onAuthError} />
        )}
      </main>
    </div>
  )
}
