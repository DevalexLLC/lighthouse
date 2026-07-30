import { useEffect, useMemo, useState } from 'react'
import type uPlot from 'uplot'
import { apiGet } from '../api'
import Chart from '../components/Chart'
import { fmtAgo, fmtLatency, fmtTime, latencyAxisLabel } from '../format'
import type {
  CurrentPath,
  DirectionSummary,
  PairResponse,
  SeriesPoint,
  SeriesResponse,
  TracerouteResponse,
  Window,
} from '../types'
import { WINDOWS } from '../types'

const POLL_MS = 60_000
type Metric = 'latency' | 'loss'

// Direction colors are categorical slots 1 (blue, outbound) and 2 (orange,
// return), stepped per color scheme; both pairs validate CVD + contrast.
const COLORS = {
  light: { aToB: '#2a78d6', bToA: '#eb6834', grid: '#e0dfd9', axis: '#55544d' },
  dark: { aToB: '#3987e5', bToA: '#d95926', grid: '#30312d', axis: '#b9b8ae' },
}

function palette() {
  return matchMedia('(prefers-color-scheme: dark)').matches ? COLORS.dark : COLORS.light
}

function toChartData(points: SeriesPoint[], metric: Metric): uPlot.AlignedData {
  const ts = points.map((p) => p.t)
  if (metric === 'loss') {
    return [ts, points.map((p) => p.loss_pct)]
  }
  const ms = (v: number | null) => (v == null ? null : v / 1000)
  return [ts, points.map((p) => ms(p.avg_us)), points.map((p) => ms(p.min_us)), points.map((p) => ms(p.max_us))]
}

function hasAnyValue(points: SeriesPoint[], metric: Metric): boolean {
  return metric === 'loss'
    ? points.some((p) => p.loss_pct != null)
    : points.some((p) => p.avg_us != null)
}

function DirectionCard({
  title,
  s,
  dir,
}: {
  title: string
  s: DirectionSummary
  dir: 'a' | 'b'
}) {
  return (
    <div className={'pair-card dir-' + dir}>
      <h3>
        <span className={'swatch series-' + dir} />
        {title}
        <span style={{ marginLeft: 'auto' }} className={'status-text-' + s.status}>
          {s.status}
        </span>
      </h3>
      <div className="pair-headline">
        <span className="big">{fmtLatency(s.latency.avg_us)}</span>
        <span className="eyebrow">
          avg {latencyAxisLabel(s.latency_source).replace(' (ms)', '')}
        </span>
      </div>
      <dl>
        <div>
          <dt>min / max</dt>
          <dd>
            {fmtLatency(s.latency.min_us)} / {fmtLatency(s.latency.max_us)}
          </dd>
        </div>
        <div>
          <dt>Loss</dt>
          <dd>{s.loss_pct == null ? '—' : s.loss_pct.toFixed(1) + '%'}</dd>
        </div>
        <div>
          <dt>Last OK</dt>
          <dd>{fmtTime(s.last_ok_at)}</dd>
        </div>
        <div>
          <dt>Samples</dt>
          <dd>{s.samples}</dd>
        </div>
      </dl>
    </div>
  )
}

// PathList renders one direction's current traceroute paths as monospace
// hop chains (a site can field several agents, so this is a list).
function PathList({ title, dir, paths }: { title: string; dir: 'a' | 'b'; paths: CurrentPath[] }) {
  return (
    <div className="path-current">
      <h4>
        <span className={'swatch series-' + dir} /> {title}
      </h4>
      {paths.length === 0 ? (
        <p className="muted">No traceroute yet. Traces run on a slower cadence.</p>
      ) : (
        paths.map((p) => (
          <div key={p.agent} className="path-chain">
            <div className="path-meta">
              <span className="mono">{p.agent}</span>
              <span className="hash-chip" title={p.path_hash}>
                {p.path_hash.slice(0, 12)}
              </span>
              <span className="hint" title={fmtTime(p.updated_at)}>
                {fmtAgo(p.updated_at)}
                {p.dest_reached ? '' : ' · incomplete'}
              </span>
            </div>
            <ol className="hops mono">
              {p.hops.map((h) => (
                <li key={h.ttl}>
                  {h.addrs.length === 0 ? '*' : h.addrs.join(', ')}
                  {h.rtt_us.length > 0 && (
                    <span className="hint"> {fmtLatency(Math.min(...h.rtt_us))}</span>
                  )}
                </li>
              ))}
            </ol>
          </div>
        ))
      )}
    </div>
  )
}

export default function PairDetail({
  a,
  b,
  onAuthError,
}: {
  a: string
  b: string
  onAuthError: (err: unknown) => void
}) {
  const [win, setWin] = useState<Window>('24h')
  const [metric, setMetric] = useState<Metric>('latency')
  const [pair, setPair] = useState<PairResponse | null>(null)
  const [series, setSeries] = useState<SeriesResponse | null>(null)
  const [paths, setPaths] = useState<TracerouteResponse | null>(null)
  const [error, setError] = useState('')

  useEffect(() => {
    let cancelled = false
    const load = () =>
      Promise.all([
        apiGet<PairResponse>(`/api/v1/pairs/${encodeURIComponent(a)}/${encodeURIComponent(b)}?window=${win}`),
        apiGet<SeriesResponse>(
          `/api/v1/pairs/${encodeURIComponent(a)}/${encodeURIComponent(b)}/series?metric=${metric}&window=${win}`,
        ),
        apiGet<TracerouteResponse>(
          `/api/v1/traceroute/${encodeURIComponent(a)}/${encodeURIComponent(b)}`,
        ),
      ])
        .then(([p, s, tr]) => {
          if (!cancelled) {
            setPair(p)
            setSeries(s)
            setPaths(tr)
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
  }, [a, b, win, metric, onAuthError])

  const mkOptions = useMemo(() => {
    return (direction: 'aToB' | 'bToA', axisLabel: string): Omit<uPlot.Options, 'width'> => {
      const c = palette()
      const stroke = c[direction]
      const axisStyle = {
        stroke: c.axis,
        grid: { stroke: c.grid, width: 1 },
        ticks: { stroke: c.grid, width: 1 },
      }
      const series: uPlot.Series[] =
        metric === 'loss'
          ? [{}, { label: 'loss %', stroke, width: 2, spanGaps: false }]
          : [
              {},
              { label: 'avg', stroke, width: 2, spanGaps: false },
              { label: 'min', stroke, width: 1, alpha: 0.4, spanGaps: false },
              { label: 'max', stroke, width: 1, alpha: 0.4, spanGaps: false },
            ]
      return {
        height: 230,
        series,
        scales: metric === 'loss' ? { y: { range: [0, 100] } } : {},
        axes: [
          { ...axisStyle },
          { ...axisStyle, label: axisLabel, size: 64 },
        ],
        cursor: { drag: { x: true, y: false } },
        legend: { live: true },
      }
    }
    // Rebuild when metric changes (series shape differs).
  }, [metric])

  if (error && !series) return <p className="error">Failed to load pair: {error}</p>
  if (!series || !pair) return <p className="muted">Loading…</p>

  const axisLabel = metric === 'loss' ? 'Loss (%)' : latencyAxisLabel(series.latency_source)
  const bucketLabel =
    series.resolution_s >= 3600
      ? `${series.resolution_s / 3600} h buckets`
      : `${series.resolution_s / 60} min buckets`

  const directions: {
    key: 'a_to_b' | 'b_to_a'
    dir: 'a' | 'b'
    chart: 'aToB' | 'bToA'
    title: string
  }[] = [
    { key: 'a_to_b', dir: 'a', chart: 'aToB', title: `${a} → ${b}` },
    { key: 'b_to_a', dir: 'b', chart: 'bToA', title: `${b} → ${a}` },
  ]

  return (
    <>
      <div className="page-head">
        <div>
          <div className="eyebrow">Pair detail</div>
          <h2>
            <a href="#/">Matrix</a> / {a} ⇄ {b}
          </h2>
        </div>
        <span className="sub">
          {bucketLabel}
          {error ? ' · refresh failed, showing last data' : ''}
        </span>
      </div>

      <div className="controls">
        <div className="control-group" role="group" aria-label="Metric">
          {(['latency', 'loss'] as const).map((m) => (
            <button key={m} className={metric === m ? 'active' : ''} onClick={() => setMetric(m)}>
              {m}
            </button>
          ))}
        </div>
        <div className="control-group" role="group" aria-label="Window">
          {WINDOWS.map((w) => (
            <button key={w} className={win === w ? 'active' : ''} onClick={() => setWin(w)}>
              {w}
            </button>
          ))}
        </div>
      </div>

      <div className="pair-cards">
        <DirectionCard title={`${a} → ${b}`} s={pair.a_to_b} dir="a" />
        <DirectionCard title={`${b} → ${a}`} s={pair.b_to_a} dir="b" />
      </div>

      <div className="card">
        <div className="card-head">
          <span className="eyebrow">Current path</span>
          <span className="hint">latest complete traceroute per direction</span>
        </div>
        <div className="path-pair">
          <PathList title={`${a} → ${b}`} dir="a" paths={paths?.a_to_b.paths ?? []} />
          <PathList title={`${b} → ${a}`} dir="b" paths={paths?.b_to_a.paths ?? []} />
        </div>
      </div>

      {directions.map(({ key, dir, chart, title }) => {
        const points = series[key].points
        return (
          <div key={key} className="card chart-card">
            <h3>
              <span className={'swatch series-' + dir} /> {title}
            </h3>
            {points.length === 0 ? (
              <div className="chart-empty">
                <p>No probe results in this window yet. New results arrive on each probe interval.</p>
              </div>
            ) : metric === 'latency' && !hasAnyValue(points, metric) ? (
              <div className="chart-empty">
                <p>
                  Every probe in this window failed, so there are no latencies to plot.{' '}
                  <button className="linklike" onClick={() => setMetric('loss')}>
                    Switch to the loss view
                  </button>{' '}
                  to see the failures over time.
                </p>
              </div>
            ) : (
              <Chart options={mkOptions(chart, axisLabel)} data={toChartData(points, metric)} />
            )}
          </div>
        )
      })}
    </>
  )
}
