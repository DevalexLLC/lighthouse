import { useEffect, useState } from 'react'
import { apiGet } from '../api'
import { fmtAgo, fmtTime } from '../format'
import type { OutageEvent, OutagesResponse, Window } from '../types'
import { WINDOWS } from '../types'

const POLL_MS = 30_000

function fmtDuration(openedAt: string, closedAt: string | null): string {
  const end = closedAt ? new Date(closedAt).getTime() : Date.now()
  const s = Math.max(0, Math.round((end - new Date(openedAt).getTime()) / 1000))
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.round(s / 60)}m`
  if (s < 86400) return `${(s / 3600).toFixed(1)}h`
  return `${(s / 86400).toFixed(1)}d`
}

function Row({ o }: { o: OutageEvent }) {
  const open = o.closed_at == null
  return (
    <tr className={open ? 'outage-open' : ''}>
      <td>
        <span className={'kind-badge kind-' + o.kind}>
          {o.kind === 'agent_offline' ? 'agent offline' : 'probe failing'}
        </span>
      </td>
      <td className="mono">
        {o.kind === 'agent_offline'
          ? `${o.src_site} · ${o.agent}`
          : `${o.src_site} → ${o.dst_site ?? o.target ?? '?'}`}
      </td>
      <td className="mono">{o.probe_type ?? '—'}</td>
      <td title={fmtTime(o.opened_at)}>{fmtAgo(o.opened_at)}</td>
      <td className={open ? 'status-text-down' : ''}>
        {open ? 'open' : fmtDuration(o.opened_at, o.closed_at)}
      </td>
      <td className="outage-error">{o.error ?? ''}</td>
    </tr>
  )
}

export default function Outages({ onAuthError }: { onAuthError: (err: unknown) => void }) {
  const [win, setWin] = useState<Window>('24h')
  const [data, setData] = useState<OutagesResponse | null>(null)
  const [error, setError] = useState('')

  useEffect(() => {
    let cancelled = false
    const load = () =>
      apiGet<OutagesResponse>(`/api/v1/outages?window=${win}`)
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
  }, [win, onAuthError])

  if (error && !data) return <p className="error">Failed to load outages: {error}</p>
  if (!data) return <p className="muted">Loading…</p>

  const openCount = data.outages.filter((o) => o.closed_at == null).length

  return (
    <>
      <div className="page-head">
        <div>
          <div className="eyebrow">Incident log</div>
          <h2>Outages</h2>
        </div>
        <div className="chips">
          <span className="chip">
            {openCount > 0 && <span className="dot swatch status-down" />}
            open <span className="mono">{openCount}</span>
          </span>
          <span className="chip">
            in window <span className="mono">{data.outages.length}</span>
          </span>
        </div>
      </div>

      <div className="controls">
        <div className="control-group" role="group" aria-label="Window">
          {WINDOWS.map((w) => (
            <button key={w} className={win === w ? 'active' : ''} onClick={() => setWin(w)}>
              {w}
            </button>
          ))}
        </div>
      </div>

      <div className="card">
        <div className="card-head">
          <span className="eyebrow">
            opens after 3 consecutive failures · closes after 3 successes
          </span>
          <span className="hint">
            open outages always shown{error ? ' · refresh failed, showing last data' : ''}
          </span>
        </div>
        {data.outages.length === 0 ? (
          <p className="muted">No outages in this window. The watch continues.</p>
        ) : (
          <div className="scroll-x">
            <table className="events">
              <thead>
                <tr>
                  <th className="eyebrow">kind</th>
                  <th className="eyebrow">where</th>
                  <th className="eyebrow">probe</th>
                  <th className="eyebrow">opened</th>
                  <th className="eyebrow">duration</th>
                  <th className="eyebrow">error</th>
                </tr>
              </thead>
              <tbody>
                {data.outages.map((o) => (
                  <Row key={o.id} o={o} />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </>
  )
}
