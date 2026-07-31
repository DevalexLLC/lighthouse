import { useEffect, useState } from 'react'
import { apiGet } from '../api'
import type { AgentsResponse, MatrixCell, MatrixResponse } from '../types'
import { fmtAgo, fmtLatency } from '../format'

const POLL_MS = 30_000

// Status is never conveyed by color alone: cells carry the status word or a
// latency figure, and the legend pairs every swatch with its label.
const STATUS_LABEL: Record<string, string> = {
  ok: 'OK',
  degraded: 'Degraded',
  down: 'Down',
  stale: 'Stale',
}

function Cell({ cell }: { cell: MatrixCell }) {
  const failed = cell.probes.filter((p) => p.status !== 'ok').length
  const total = cell.probes.length
  const detail = cell.probes
    .map(
      (p) =>
        `${p.type}: ${p.status}` +
        `${p.latency_us != null ? ` · ${fmtLatency(p.latency_us)}` : ''}` +
        `${p.loss_pct != null && p.loss_pct > 0 ? ` · ${p.loss_pct.toFixed(0)}% loss` : ''}`,
    )
    .join(', ')
  const healthy = cell.status === 'ok' || cell.status === 'degraded'
  const checksWord = total === 1 ? 'check' : 'checks'
  // Partial packet loss can ride on an all-OK cell; keep it at a glance.
  const loss = healthy && cell.loss_pct != null && cell.loss_pct > 0 ? ` · ${cell.loss_pct.toFixed(0)}% loss` : ''
  const sub =
    cell.status === 'stale'
      ? 'no recent data'
      : cell.status === 'ok'
        ? `${total} ${checksWord} OK${loss}`
        : `${failed} of ${total} ${checksWord} failed${loss}`
  return (
    <td className={'cell status-' + cell.status}>
      <a
        href={`#/pair/${encodeURIComponent(cell.src)}/${encodeURIComponent(cell.dst)}`}
        title={`${cell.src} → ${cell.dst} · ${STATUS_LABEL[cell.status]} · ${detail}`}
        aria-label={`${cell.src} to ${cell.dst}: ${STATUS_LABEL[cell.status]}. ${sub}. ${detail}`}
      >
        <span className="cell-value">
          {healthy ? fmtLatency(cell.latency_us) : STATUS_LABEL[cell.status]}
        </span>
        <span className="cell-sub">
          {sub}
        </span>
      </a>
    </td>
  )
}

export default function Matrix({ onAuthError }: { onAuthError: (err: unknown) => void }) {
  const [data, setData] = useState<MatrixResponse | null>(null)
  const [agents, setAgents] = useState<AgentsResponse | null>(null)
  const [error, setError] = useState('')

  useEffect(() => {
    let cancelled = false
    const load = () =>
      Promise.all([apiGet<MatrixResponse>('/api/v1/matrix'), apiGet<AgentsResponse>('/api/v1/agents')])
        .then(([m, a]) => {
          if (!cancelled) {
            setData(m)
            setAgents(a)
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

  if (error && !data) return <p className="error">Failed to load sightlines: {error}</p>
  if (!data) return <p className="muted">Loading…</p>

  const cellFor = new Map<string, MatrixCell>()
  for (const c of data.cells) cellFor.set(c.src + ' ' + c.dst, c)
  const sites = data.sites
  const counts = { ok: 0, degraded: 0, down: 0, stale: 0 }
  for (const c of data.cells) counts[c.status]++

  return (
    <>
      <div className="page-head">
        <div>
          <div className="eyebrow">Signal board</div>
          <h2>Sightlines</h2>
        </div>
        <div className="chips">
          <span className="chip">
            sites <span className="mono">{sites.length}</span>
          </span>
          {(['ok', 'degraded', 'down', 'stale'] as const).map(
            (s) =>
              counts[s] > 0 && (
                <span key={s} className="chip">
                  <span className={'dot swatch status-' + s} />
                  {STATUS_LABEL[s].toLowerCase()} <span className="mono">{counts[s]}</span>
                </span>
              ),
          )}
        </div>
      </div>

      <div className="card">
        <div className="card-head">
          <span className="eyebrow">Source sites probe destination sites</span>
          <span className="hint">
            last {Math.round(data.horizon_s / 60)} min
            {error ? ' · refresh failed, showing last data' : ''}
          </span>
        </div>
        {sites.length < 2 ? (
          <p className="muted">
            Fewer than two sites are enrolled. Enroll agents at a second site and add both to a
            mesh group to light up the board.
          </p>
        ) : (
          <div className="scroll-x">
            <table className="matrix">
              <thead>
                <tr>
                  <th className="corner eyebrow" scope="col">
                    source ↓<br />destination →
                  </th>
                  {sites.map((s) => (
                    <th key={s.name} scope="col">
                      {s.display_name || s.name}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {sites.map((src) => (
                  <tr key={src.name}>
                    <th scope="row">{src.display_name || src.name}</th>
                    {sites.map((dst) => {
                      if (src.name === dst.name)
                        return <td key={dst.name} className="diag" aria-label="same site" />
                      const cell = cellFor.get(src.name + ' ' + dst.name)
                      if (!cell)
                        return (
                          <td key={dst.name} className="empty">
                            not probed
                          </td>
                        )
                      return <Cell key={dst.name} cell={cell} />
                    })}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        <div className="legend">
          {(['ok', 'degraded', 'down', 'stale'] as const).map((s) => (
            <span key={s} className="legend-item">
              <span className={'swatch status-' + s} /> {STATUS_LABEL[s]}
            </span>
          ))}
        </div>
      </div>

      <div className="card">
        <div className="card-head">
          <span className="eyebrow">Agents</span>
          <span className="hint">an agent is live when its config stream is connected</span>
        </div>
        {!agents || agents.agents.length === 0 ? (
          <p className="muted">No agents enrolled yet.</p>
        ) : (
          <div className="scroll-x">
            <table className="agents">
              <thead>
                <tr>
                  <th className="eyebrow">site</th>
                  <th className="eyebrow">hostname</th>
                  <th className="eyebrow">probe address</th>
                  <th className="eyebrow">version</th>
                  <th className="eyebrow">last seen</th>
                </tr>
              </thead>
              <tbody>
                {agents.agents.map((a) => {
                  const seconds = a.last_seen_at
                    ? (Date.now() - new Date(a.last_seen_at).getTime()) / 1000
                    : Infinity
                  return (
                    <tr key={a.id}>
                      <td className="mono" data-label="Site">{a.site}</td>
                      <td className="mono" data-label="Hostname">{a.hostname}</td>
                      <td className="mono" data-label="Probe address">{a.probe_address || '—'}</td>
                      <td className="mono" data-label="Version">{a.version || '—'}</td>
                      <td data-label="Last seen" className={seconds < 120 ? 'seen-live' : 'seen-gone'}>
                        {fmtAgo(a.last_seen_at)}
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </>
  )
}
