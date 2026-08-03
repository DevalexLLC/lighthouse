import { useState, type FormEvent } from 'react'
import { ApiError, apiPost } from '../api'
import LogoMark from '../components/LogoMark'
import type { LoginResponse } from '../types'

export default function Login({ onLogin }: { onLogin: (res: LoginResponse) => void }) {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [showPassword, setShowPassword] = useState(false)

  async function submit(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      const res = await apiPost<LoginResponse>('/api/v1/auth/login', { username, password })
      onLogin(res)
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        setError('Invalid username or password.')
      } else if (err instanceof ApiError && err.status === 429) {
        setError('Too many attempts — wait a minute and try again.')
      } else {
        setError('Login failed: ' + (err instanceof Error ? err.message : String(err)))
      }
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="login-wrap">
      <section className="login-context" aria-label="Product introduction">
        <LogoMark className="logo-mark login-context-mark" />
        <h1>See the network clearly.</h1>
        <p>
          Monitor inter-site connectivity, correlate incidents, and investigate directional performance from one control
          plane.
        </p>
        <div className="login-signals" aria-hidden="true">
          <span />
          <span />
          <span />
          <span />
        </div>
      </section>
      <form className="login-card" onSubmit={submit} aria-busy={busy}>
        <div className="login-mark">
          <LogoMark className="logo-mark logo-mark-login" />
          <h1>Lighthouse</h1>
        </div>
        <p className="login-sub">Sign in to the Lighthouse control plane.</p>
        <label className="eyebrow">
          Username
          <input autoFocus autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} />
        </label>
        <label className="eyebrow">
          Password
          <span className="password-field">
            <input
              type={showPassword ? 'text' : 'password'}
              autoComplete="current-password"
              value={password}
              aria-invalid={Boolean(error)}
              aria-describedby={error ? 'login-error' : undefined}
              onChange={(e) => setPassword(e.target.value)}
            />
            <button
              type="button"
              className="password-toggle"
              aria-pressed={showPassword}
              onClick={() => setShowPassword(!showPassword)}
            >
              {showPassword ? 'Hide' : 'Show'}
            </button>
          </span>
        </label>
        {error && (
          <p className="error login-error" id="login-error" role="alert">
            {error}
          </p>
        )}
        <button type="submit" disabled={busy || !username || !password}>
          {busy ? 'Signing in…' : 'Sign in'}
        </button>
      </form>
    </div>
  )
}
