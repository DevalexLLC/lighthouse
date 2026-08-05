import { useCallback, useEffect, useState } from 'react'
import { ApiError, apiGet, apiPost, setCsrfToken } from './api'
import type { AuthProviders, LoginResponse, User } from './types'
import LogoMark from './components/LogoMark'
import ThemeToggle from './components/ThemeToggle'
import Agents from './views/Agents'
import Login from './views/Login'
import Overview from './views/Overview'
import Outages from './views/Outages'
import PairDetail from './views/PairDetail'
import Paths from './views/Paths'
import Settings from './views/Settings'

// Hash routing stays dependency-free and preserves the original route names
// as aliases, so bookmarks survive the information-architecture cleanup.
export type SettingsTab = 'thresholds' | 'targets' | 'meshes' | 'probes' | 'authentication'

const SETTINGS_TABS: SettingsTab[] = ['thresholds', 'targets', 'meshes', 'probes', 'authentication']

type Route =
  | { view: 'overview' }
  | { view: 'pair'; a: string; b: string }
  | { view: 'incidents' }
  | { view: 'routes' }
  | { view: 'agents' }
  | { view: 'settings'; tab: SettingsTab }

function parseHash(hash: string): Route {
  const parts = hash.replace(/^#\/?/, '').split('/')
  if (parts[0] === 'pair' && parts[1] && parts[2]) {
    return { view: 'pair', a: decodeURIComponent(parts[1]), b: decodeURIComponent(parts[2]) }
  }
  if (parts[0] === 'incidents' || parts[0] === 'outages') return { view: 'incidents' }
  if (parts[0] === 'routes' || parts[0] === 'paths') return { view: 'routes' }
  if (parts[0] === 'agents') return { view: 'agents' }
  if (parts[0] === 'settings') {
    // #/settings/<tab>; unknown or absent tabs land on thresholds so the
    // plain #/settings bookmark (and the user-menu link) keep working.
    const tab = SETTINGS_TABS.find((t) => t === parts[1]) ?? 'thresholds'
    return { view: 'settings', tab }
  }
  // #/connectivity and #/sightlines land here too: the map/matrix switch
  // lives on the Overview now, so those bookmarks alias to it.
  return { view: 'overview' }
}

const NAV: Array<{ href: string; label: string; isActive: (r: Route) => boolean }> = [
  // Pair detail is reached from the Overview matrix, so it keeps Overview lit.
  { href: '#/', label: 'Overview', isActive: (r) => r.view === 'overview' || r.view === 'pair' },
  { href: '#/incidents', label: 'Incidents', isActive: (r) => r.view === 'incidents' },
  { href: '#/routes', label: 'Routes', isActive: (r) => r.view === 'routes' },
  { href: '#/agents', label: 'Agents', isActive: (r) => r.view === 'agents' },
]

export default function App() {
  const [booted, setBooted] = useState(false)
  const [user, setUser] = useState<User | null>(null)
  const [sso, setSso] = useState(false)
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

  // Learn whether to offer single sign-on every time the login screen is
  // entered (boot without a session, and again after logout), so an admin
  // toggling OIDC converges without a reload. A providers failure only
  // hides the SSO button — local login must never depend on it.
  useEffect(() => {
    if (!booted || user !== null) return
    apiGet<AuthProviders>('/api/v1/auth/providers')
      .then((res) => setSso(res.oidc.enabled))
      .catch((err) => {
        console.warn('auth providers unavailable; hiding SSO login', err)
        setSso(false)
      })
  }, [booted, user])

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
        <LogoMark className="logo-mark logo-mark-boot" />
        Loading Lighthouse…
      </div>
    )
  }
  if (!user) {
    return (
      <Login
        sso={sso}
        onLogin={(res) => {
          setCsrfToken(res.csrf_token)
          setUser(res.user)
        }}
      />
    )
  }

  return (
    <div className="app">
      <button className="skip-link" onClick={() => document.getElementById('main-content')?.focus()}>
        Skip to content
      </button>
      <header className="topbar">
        <a className="brand" href="#/">
          <LogoMark className="logo-mark logo-mark-header" />
          Lighthouse
        </a>
        <nav className="topnav" aria-label="Primary navigation">
          {NAV.map((item) => (
            <a
              key={item.href}
              href={item.href}
              className={item.isActive(route) ? 'active' : ''}
              aria-current={item.isActive(route) ? 'page' : undefined}
            >
              {item.label}
            </a>
          ))}
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
              <button type="button" onClick={logout}>
                Log out
              </button>
            </div>
          </details>
        </div>
      </header>
      <main id="main-content" tabIndex={-1}>
        {route.view === 'overview' ? (
          <Overview onAuthError={onAuthError} />
        ) : route.view === 'pair' ? (
          <PairDetail a={route.a} b={route.b} onAuthError={onAuthError} />
        ) : route.view === 'incidents' ? (
          <Outages onAuthError={onAuthError} />
        ) : route.view === 'agents' ? (
          <Agents onAuthError={onAuthError} />
        ) : route.view === 'settings' ? (
          <Settings tab={route.tab} isAdmin={user.role === 'admin'} onAuthError={onAuthError} />
        ) : (
          <Paths onAuthError={onAuthError} />
        )}
      </main>
    </div>
  )
}
