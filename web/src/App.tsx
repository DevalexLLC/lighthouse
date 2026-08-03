import { useCallback, useEffect, useState } from 'react'
import { ApiError, apiGet, apiPost, setCsrfToken } from './api'
import type { LoginResponse, User } from './types'
import ThemeToggle from './components/ThemeToggle'
import Agents from './views/Agents'
import Login from './views/Login'
import Matrix from './views/Matrix'
import Overview from './views/Overview'
import Outages from './views/Outages'
import PairDetail from './views/PairDetail'
import Paths from './views/Paths'
import Settings from './views/Settings'

// Hash routing stays dependency-free and preserves the original route names
// as aliases, so bookmarks survive the information-architecture cleanup.
type Route =
  | { view: 'overview' }
  | { view: 'connectivity' }
  | { view: 'pair'; a: string; b: string }
  | { view: 'incidents' }
  | { view: 'routes' }
  | { view: 'agents' }
  | { view: 'settings' }

function parseHash(hash: string): Route {
  const parts = hash.replace(/^#\/?/, '').split('/')
  if (parts[0] === 'pair' && parts[1] && parts[2]) {
    return { view: 'pair', a: decodeURIComponent(parts[1]), b: decodeURIComponent(parts[2]) }
  }
  if (parts[0] === 'connectivity' || parts[0] === 'sightlines') return { view: 'connectivity' }
  if (parts[0] === 'incidents' || parts[0] === 'outages') return { view: 'incidents' }
  if (parts[0] === 'routes' || parts[0] === 'paths') return { view: 'routes' }
  if (parts[0] === 'agents') return { view: 'agents' }
  if (parts[0] === 'settings') return { view: 'settings' }
  return { view: 'overview' }
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
      <button
        className="skip-link"
        onClick={() => document.getElementById('main-content')?.focus()}
      >
        Skip to content
      </button>
      <header className="topbar">
        <a className="brand" href="#/">
          <img className="logo-mark logo-mark-header" src="/lighthouse-mark.svg" alt="" />
          Lighthouse
        </a>
        <nav className="topnav" aria-label="Primary navigation">
          <a
            href="#/"
            className={route.view === 'overview' ? 'active' : ''}
            aria-current={route.view === 'overview' ? 'page' : undefined}
          >
            Overview
          </a>
          <a
            href="#/connectivity"
            className={route.view === 'connectivity' || route.view === 'pair' ? 'active' : ''}
            aria-current={route.view === 'connectivity' || route.view === 'pair' ? 'page' : undefined}
          >
            Connectivity
          </a>
          <a
            href="#/incidents"
            className={route.view === 'incidents' ? 'active' : ''}
            aria-current={route.view === 'incidents' ? 'page' : undefined}
          >
            Incidents
          </a>
          <a
            href="#/routes"
            className={route.view === 'routes' ? 'active' : ''}
            aria-current={route.view === 'routes' ? 'page' : undefined}
          >
            Routes
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
          <ThemeToggle />
          <details className={'user-menu' + (route.view === 'settings' ? ' user-menu-current' : '')}>
            <summary aria-label={`Open user menu for ${user.username}`}>
              <svg className="user-menu-icon" viewBox="0 0 24 24" aria-hidden="true">
                <circle cx="12" cy="8" r="3.5" />
                <path d="M5.5 20c.5-4 2.7-6 6.5-6s6 2 6.5 6" />
              </svg>
            </summary>
            <div className="user-menu-popover">
              <div className="user-menu-identity">
                <strong>{user.username}</strong>
                <span>{user.role}</span>
              </div>
              {user.role === 'admin' && (
                <a
                  href="#/settings"
                  aria-current={route.view === 'settings' ? 'page' : undefined}
                  onClick={(event) => event.currentTarget.closest('details')?.removeAttribute('open')}
                >
                  Settings
                </a>
              )}
              <button type="button" onClick={logout}>Log out</button>
            </div>
          </details>
        </div>
      </header>
      <main id="main-content" tabIndex={-1}>
        {route.view === 'overview' ? (
          <Overview onAuthError={onAuthError} />
        ) : route.view === 'connectivity' ? (
          <Matrix onAuthError={onAuthError} />
        ) : route.view === 'pair' ? (
          <PairDetail a={route.a} b={route.b} onAuthError={onAuthError} />
        ) : route.view === 'incidents' ? (
          <Outages onAuthError={onAuthError} />
        ) : route.view === 'agents' ? (
          <Agents onAuthError={onAuthError} />
        ) : route.view === 'settings' ? (
          <Settings
            isAdmin={user.role === 'admin'}
            onAuthError={onAuthError}
          />
        ) : (
          <Paths onAuthError={onAuthError} />
        )}
      </main>
    </div>
  )
}
