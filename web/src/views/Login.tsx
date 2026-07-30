import { useState, type FormEvent } from 'react'
import { ApiError, apiPost } from '../api'
import type { LoginResponse } from '../types'

export default function Login({ onLogin }: { onLogin: (res: LoginResponse) => void }) {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

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
      <form className="login-card" onSubmit={submit}>
        <h1>Lighthouse</h1>
        <label>
          Username
          <input
            autoFocus
            autoComplete="username"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
          />
        </label>
        <label>
          Password
          <input
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </label>
        {error && (
          <p className="error" role="alert">
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
