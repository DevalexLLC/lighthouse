import { useEffect, useState } from 'react'
import { apiGet } from '../api'
import WorldMap from '../components/WorldMap'
import { directionSeverity, SEVERITY_LABEL, type Severity } from '../severity'
import type { MatrixCell, MatrixResponse, SettingsResponse, ThresholdSettings } from '../types'
import { fmtLatency } from '../format'

const POLL_MS = 30_000

// Matrix cells grade through the same directionSeverity fold as the map and
// Overview — the raw API status alone would call a threshold-violating
// direction "Healthy" while the other two views show it Degraded. Severity →
// cell visual class: warn and crit both render the shared "Degraded"
// treatment (crit's stronger intensity lives in detailed views).
type CellClass = 'ok' | 'degraded' | 'down' | 'stale'
const SEV_CLASS: Record<Severity, CellClass> = {
  ok: 'ok',
  warn: 'degraded',
  crit: 'degraded',
  down: 'down',
  stale: 'stale',
}

// Status is never conveyed by color alone: cells carry the status word or a
// latency figure, and the legend pairs every swatch with its label.
const CLASS_LABEL: Record<CellClass, string> = {
  ok: SEVERITY_LABEL.ok,
  degraded: SEVERITY_LABEL.warn,
  down: SEVERITY_LABEL.down,
  stale: SEVERITY_LABEL.stale,
}

function Cell({ cell, thresholds }: { cell: MatrixCell; thresholds: ThresholdSettings | null }) {
  const cls = SEV_CLASS[directionSeverity(cell, thresholds)]
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
  const hasLatency = cell.status === 'ok' || cell.status === 'degraded'
  const checks =
    cell.status === 'stale'
      ? 'No recent data'
      : cell.status === 'ok'
        ? `${total}/${total} checks healthy`
        : `${failed}/${total} checks failed`
  // The API intentionally reports the best working latency and the worst
  // probe loss. Label that fold explicitly so mixed checks never read as a
  // single direction simultaneously succeeding and losing every packet.
  const worstLoss = cell.loss_pct != null && cell.loss_pct > 0
    ? ` · worst probe ${cell.loss_pct.toFixed(0)}% loss`
    : ''
  return (
    <td className={'cell status-' + cls}>
      <a
        href={`#/pair/${encodeURIComponent(cell.src)}/${encodeURIComponent(cell.dst)}`}
        title={`${cell.src} → ${cell.dst} · ${CLASS_LABEL[cls]} · ${detail}`}
        aria-label={`${cell.src} to ${cell.dst}: ${CLASS_LABEL[cls]}. ${checks}${worstLoss}. ${detail}`}
      >
        <span className="cell-status">
          <span className={'dot swatch status-' + cls} />
          {CLASS_LABEL[cls]}
        </span>
        <span className="cell-value">
          {hasLatency ? fmtLatency(cell.latency_us) : '—'}
        </span>
        <span className="cell-sub">
          {checks}{worstLoss}
        </span>
      </a>
    </td>
  )
}

export default function Matrix({ onAuthError }: { onAuthError: (err: unknown) => void }) {
  const [data, setData] = useState<MatrixResponse | null>(null)
  const [settings, setSettings] = useState<SettingsResponse | null>(null)
  const [mode, setMode] = useState<'map' | 'matrix'>('map')
  const [error, setError] = useState('')

  useEffect(() => {
    let cancelled = false
    const load = () =>
      // Settings ride the same poll, so another admin's change converges
      // everywhere within one cycle.
      Promise.all([
        apiGet<MatrixResponse>('/api/v1/matrix'),
        apiGet<SettingsResponse>('/api/v1/settings'),
      ])
        .then(([m, s]) => {
          if (!cancelled) {
            setData(m)
            setSettings(s)
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

  if (error && !data) return <div className="state-panel state-error"><h1>Connectivity unavailable</h1><p>{error}</p></div>
  if (!data) return <div className="state-panel" role="status"><span className="state-spinner" />Loading connectivity…</div>

  const cellFor = new Map<string, MatrixCell>()
  for (const c of data.cells) cellFor.set(c.src + ' ' + c.dst, c)
  const sites = data.sites
  const thresholds = settings?.thresholds ?? null
  const counts: Record<CellClass, number> = { ok: 0, degraded: 0, down: 0, stale: 0 }
  for (const c of data.cells) counts[SEV_CLASS[directionSeverity(c, thresholds)]]++

  return (
    <>
      <div className="page-head page-head-primary">
        <div>
          <div className="eyebrow">Operations</div>
          <h1>Connectivity</h1>
          <p>Inspect the topology or compare every monitored direction.</p>
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
                  {CLASS_LABEL[s].toLowerCase()} <span className="mono">{counts[s]}</span>
                </span>
              ),
          )}
        </div>
      </div>

      {error && <div className="inline-alert" role="status">Refresh failed. Showing the last successful snapshot.</div>}

      <div className="view-toolbar">
        <div className="control-group" role="group" aria-label="Connectivity view">
          <button className={mode === 'map' ? 'active' : ''} aria-pressed={mode === 'map'} onClick={() => setMode('map')}>Map</button>
          <button className={mode === 'matrix' ? 'active' : ''} aria-pressed={mode === 'matrix'} onClick={() => setMode('matrix')}>Matrix</button>
        </div>
        <span className="freshness">Latest {Math.round(data.horizon_s / 60)}-minute probe horizon</span>
      </div>

      {mode === 'map' ? (
        <div className="card connectivity-map-card">
          <div className="card-head">
            <div><span className="eyebrow">Topology</span><h2>Site map</h2></div>
            <span className="hint">Select a site to isolate its links</span>
          </div>
          <WorldMap sites={sites} cells={data.cells} thresholds={settings?.thresholds ?? null} />
        </div>
      ) : (
      <div className="card connectivity-matrix-card">
        <div className="card-head">
          <div><span className="eyebrow">Directional comparison</span><h2>Connectivity matrix</h2></div>
          <span className="hint">
            Rows are sources · columns are destinations
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
                      return <Cell key={dst.name} cell={cell} thresholds={thresholds} />
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
              <span className={'swatch status-' + s} /> {CLASS_LABEL[s]}
            </span>
          ))}
        </div>
      </div>
      )}
    </>
  )
}
