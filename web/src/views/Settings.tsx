import { useEffect, useState } from 'react'
import { apiGet } from '../api'
import ThresholdSettingsPanel from '../components/ThresholdSettings'
import type { SettingsResponse } from '../types'

const POLL_MS = 30_000

export default function Settings({
  isAdmin,
  onAuthError,
}: {
  isAdmin: boolean
  onAuthError: (err: unknown) => void
}) {
  const [settings, setSettings] = useState<SettingsResponse | null>(null)
  const [error, setError] = useState('')

  // Poll like every other view: a transient failure retries on the next
  // tick, and another admin's change converges here ≤30 s. The panel keeps
  // its own draft once edited, so a poll never clobbers in-progress input.
  useEffect(() => {
    let cancelled = false
    const load = () => {
      apiGet<SettingsResponse>('/api/v1/settings')
        .then((s) => {
          if (!cancelled) {
            setSettings(s)
            setError('')
          }
        })
        .catch((err) => {
          if (cancelled) return
          onAuthError(err)
          setError(err instanceof Error ? err.message : String(err))
        })
    }
    load()
    const id = setInterval(load, POLL_MS)
    return () => {
      cancelled = true
      clearInterval(id)
    }
  }, [onAuthError])

  return (
    <>
      <div className="page-head page-head-primary">
        <div>
          <div className="eyebrow">Administration</div>
          <h1>Settings</h1>
          <p>Shared thresholds used to classify network health across the dashboard.</p>
        </div>
      </div>
      {error && !settings ? (
        <div className="state-panel state-error"><h2>Settings unavailable</h2><p>{error}</p></div>
      ) : !settings ? (
        <div className="state-panel" role="status"><span className="state-spinner" />Loading settings…</div>
      ) : (
        <>
        {error && <div className="inline-alert" role="status">Refresh failed. Showing the last successful snapshot.</div>}
        <section className="card settings-card">
          <div className="card-head">
            <div><span className="eyebrow">Health classification</span><h2>Connectivity thresholds</h2></div>
          </div>
          <p className="section-intro">Values at or above the degraded threshold require attention. Critical thresholds remain a stronger visual signal inside detailed connectivity views.</p>
          <ThresholdSettingsPanel
            settings={settings}
            isAdmin={isAdmin}
            onSaved={setSettings}
            onAuthError={onAuthError}
            variant="page"
          />
        </section>
        </>
      )}
    </>
  )
}
