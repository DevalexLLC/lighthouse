import { useEffect, useState } from 'react'
import { apiGet } from '../api'
import { fmtAgo, fmtTime } from '../format'
import type { AgentInfo, AgentsResponse } from '../types'

const POLL_MS = 30_000
const CERT_WARN_DAYS = 30

type Health = 'ok' | 'degraded' | 'down' | 'stale'

// Health folds the server's signals in severity order: an unusable cert
// (revoked or expired) or an open agent_offline outage beats everything;
// failing probe series degrade; an agent that has never connected is
// stale, not broken. Expiry is checked here too because the offline sweep
// takes minutes to notice a cut-off agent — the row must not say ok while
// the certificate cell says expired.
function health(a: AgentInfo): { status: Health; label: string } {
  if (a.cert_revoked_at) return { status: 'down', label: 'revoked' }
  if (a.cert_not_after && certDaysLeft(a.cert_not_after) < 0)
    return { status: 'down', label: 'cert expired' }
  if (a.offline) return { status: 'down', label: 'offline' }
  if (!a.last_seen_at) return { status: 'stale', label: 'never seen' }
  if (a.probes_failing > 0) return { status: 'degraded', label: 'degraded' }
  return { status: 'ok', label: 'ok' }
}

function certDaysLeft(notAfter: string): number {
  return Math.floor((new Date(notAfter).getTime() - Date.now()) / 86_400_000)
}

function CertCell({ a }: { a: AgentInfo }) {
  if (a.cert_revoked_at)
    return (
      <span className="status-text-down" title={fmtTime(a.cert_revoked_at)}>
        revoked {fmtAgo(a.cert_revoked_at)}
      </span>
    )
  if (!a.cert_not_after) return <span className="hint">—</span>
  const days = certDaysLeft(a.cert_not_after)
  if (days < 0)
    return (
      <span className="status-text-down" title={fmtTime(a.cert_not_after)}>
        expired
      </span>
    )
  if (days <= CERT_WARN_DAYS)
    return (
      <span className="status-text-degraded" title={fmtTime(a.cert_not_after)}>
        expires in {days}d
      </span>
    )
  return (
    <span className="muted" title={fmtTime(a.cert_not_after)}>
      {days}d left
    </span>
  )
}

function Row({ a }: { a: AgentInfo }) {
  const h = health(a)
  return (
    <tr>
      <td data-label="Status">
        <span className={'status-text-' + h.status}>
          <span className={'dot swatch status-' + h.status} /> {h.label}
        </span>
      </td>
      <td className="mono" data-label="Agent" title={`enrolled ${fmtTime(a.enrolled_at)} · ${a.id}`}>
        {a.site} · {a.hostname}
      </td>
      <td className="mono" data-label="Address">{a.probe_address || '—'}</td>
      <td className="mono" data-label="Version">{a.version || '—'}</td>
      <td data-label="Last seen" title={fmtTime(a.last_seen_at)}>
        {fmtAgo(a.last_seen_at)}
      </td>
      <td data-label="Probes">
        {a.probes_total === 0 ? (
          <span className="hint">none yet</span>
        ) : a.probes_failing > 0 ? (
          <span className="status-text-degraded">
            {a.probes_failing} of {a.probes_total} failing
          </span>
        ) : (
          <span className="muted">{a.probes_total} ok</span>
        )}
      </td>
      <td data-label="Spool drops">
        {a.dropped_results === 0 ? (
          <span className="muted">none</span>
        ) : (
          <span
            className="status-text-degraded"
            title={a.last_dropped_at ? `last ${fmtTime(a.last_dropped_at)}` : undefined}
          >
            {a.dropped_results.toLocaleString()} lost · {fmtAgo(a.last_dropped_at)}
          </span>
        )}
      </td>
      <td data-label="Certificate">
        <CertCell a={a} />
      </td>
      <td className="mono" data-label="Config" title={a.config_hash || undefined}>
        {a.config_hash ? a.config_hash.slice(0, 8) : '—'}
      </td>
    </tr>
  )
}

export default function Agents({ onAuthError }: { onAuthError: (err: unknown) => void }) {
  const [data, setData] = useState<AgentsResponse | null>(null)
  const [error, setError] = useState('')

  useEffect(() => {
    let cancelled = false
    const load = () =>
      apiGet<AgentsResponse>('/api/v1/agents')
        .then((res) => {
          if (!cancelled) {
            setData(res)
            setError('')
          }
        })
        .catch((err) => {
          onAuthError(err)
          if (!cancelled) setError(err instanceof Error ? err.message : String(err))
        })
    load()
    const id = setInterval(load, POLL_MS)
    return () => {
      cancelled = true
      clearInterval(id)
    }
  }, [onAuthError])

  if (error && !data) return <p className="error">Failed to load agents: {error}</p>
  if (!data) return <p className="muted">Loading…</p>

  const down = data.agents.filter((a) => health(a).status === 'down').length
  const degraded = data.agents.filter((a) => health(a).status === 'degraded').length
  const dropsTotal = data.agents.reduce((sum, a) => sum + a.dropped_results, 0)

  return (
    <>
      <div className="page-head">
        <div>
          <div className="eyebrow">Fleet health</div>
          <h2>Agents</h2>
        </div>
        <div className="chips">
          <span className="chip">
            enrolled <span className="mono">{data.agents.length}</span>
          </span>
          <span className="chip">
            {down > 0 && <span className="dot swatch status-down" />}
            down <span className="mono">{down}</span>
          </span>
          <span className="chip">
            {degraded > 0 && <span className="dot swatch status-degraded" />}
            degraded <span className="mono">{degraded}</span>
          </span>
          <span className="chip">
            results lost <span className="mono">{dropsTotal.toLocaleString()}</span>
          </span>
        </div>
      </div>

      <div className="card">
        <div className="card-head">
          <span className="eyebrow">
            one keeper per site host · refreshed every {POLL_MS / 1000}s
          </span>
          <span className="hint">
            spool drops are lifetime totals{error ? ' · refresh failed, showing last data' : ''}
          </span>
        </div>
        {data.agents.length === 0 ? (
          <p className="muted">No agents enrolled yet.</p>
        ) : (
          <div className="scroll-x">
            <table className="events">
              <thead>
                <tr>
                  <th className="eyebrow">status</th>
                  <th className="eyebrow">agent</th>
                  <th className="eyebrow">address</th>
                  <th className="eyebrow">version</th>
                  <th className="eyebrow">last seen</th>
                  <th className="eyebrow">probes</th>
                  <th className="eyebrow">spool drops</th>
                  <th className="eyebrow">certificate</th>
                  <th className="eyebrow">config</th>
                </tr>
              </thead>
              <tbody>
                {data.agents.map((a) => (
                  <Row key={a.id} a={a} />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </>
  )
}
