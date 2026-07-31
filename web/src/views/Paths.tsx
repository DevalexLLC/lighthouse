import { useEffect, useState } from 'react'
import { apiGet } from '../api'
import { fmtAgo, fmtTime } from '../format'
import type { Hop, PathEvent, PathEventsResponse, Window } from '../types'
import { WINDOWS } from '../types'

const POLL_MS = 30_000

export function hopLabel(h: Hop | undefined): string {
  if (!h || h.addrs.length === 0) return '*'
  return h.addrs.join(', ')
}

// HopDiff renders old and new hop lists side by side, one row per TTL,
// highlighting rows whose responder set changed.
function HopDiff({ oldHops, newHops }: { oldHops: Hop[]; newHops: Hop[] }) {
  const rows = Math.max(oldHops.length, newHops.length)
  const byTTL = (hops: Hop[], ttl: number) => hops.find((h) => h.ttl === ttl)
  return (
    <div className="scroll-x">
      <table className="hop-diff">
        <thead>
          <tr>
            <th className="eyebrow">ttl</th>
            <th className="eyebrow">old path</th>
            <th className="eyebrow">new path</th>
          </tr>
        </thead>
        <tbody>
          {Array.from({ length: rows }, (_, i) => {
            const ttl = i + 1
            const o = byTTL(oldHops, ttl)
            const n = byTTL(newHops, ttl)
            const changed = hopLabel(o) !== hopLabel(n)
            return (
              <tr key={ttl} className={changed ? 'hop-changed' : ''}>
                <td className="mono">{ttl}</td>
                <td className="mono">{hopLabel(o)}</td>
                <td className="mono">{hopLabel(n)}</td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}

function EventRow({ e }: { e: PathEvent }) {
  const [expanded, setExpanded] = useState(false)
  return (
    <div className="path-event">
      <button className="path-event-head" onClick={() => setExpanded(!expanded)}>
        <span className="mono">
          {e.src_site} → {e.dst_site ?? e.target ?? '?'}
        </span>
        <span className="hash-chip" title={e.old_path_hash}>
          {e.old_path_hash.slice(0, 12)}
        </span>
        <span aria-hidden="true">→</span>
        <span className="hash-chip" title={e.new_path_hash}>
          {e.new_path_hash.slice(0, 12)}
        </span>
        <span className="hint" title={fmtTime(e.time)}>
          {fmtAgo(e.time)}
        </span>
        <span className="path-toggle">
          <span aria-hidden="true">{expanded ? '▾' : '▸'}</span> {expanded ? 'hide diff' : 'show diff'}
        </span>
      </button>
      {expanded && <HopDiff oldHops={e.old_hops} newHops={e.new_hops} />}
    </div>
  )
}

export default function Paths({ onAuthError }: { onAuthError: (err: unknown) => void }) {
  const [win, setWin] = useState<Window>('24h')
  const [data, setData] = useState<PathEventsResponse | null>(null)
  const [error, setError] = useState('')

  useEffect(() => {
    let cancelled = false
    const load = () =>
      apiGet<PathEventsResponse>(`/api/v1/path-events?window=${win}`)
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

  if (error && !data) return <p className="error">Failed to load path events: {error}</p>
  if (!data) return <p className="muted">Loading…</p>

  return (
    <>
      <div className="page-head">
        <div>
          <div className="eyebrow">Route watch</div>
          <h2>Path changes</h2>
        </div>
        <div className="chips">
          <span className="chip">
            in window <span className="mono">{data.events.length}</span>
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
          <span className="eyebrow">a change is a new sha256 over the hop sequence</span>
          <span className="hint">
            traceroutes run on a slower cadence than other probes
            {error ? ' · refresh failed, showing last data' : ''}
          </span>
        </div>
        {data.events.length === 0 ? (
          <p className="muted">No path changes in this window. Routes are holding.</p>
        ) : (
          data.events.map((e) => <EventRow key={e.id} e={e} />)
        )}
      </div>
    </>
  )
}
