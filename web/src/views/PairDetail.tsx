import { useEffect, useMemo, useState } from 'react'
import type uPlot from 'uplot'
import { apiGet } from '../api'
import Chart from '../components/Chart'
import { fmtLatency, fmtTime, latencyAxisLabel } from '../format'
import type { DirectionSummary, PairResponse, SeriesPoint, SeriesResponse, Window } from '../types'
import { WINDOWS } from '../types'

const POLL_MS = 60_000
type Metric = 'latency' | 'loss'

// Direction colors are categorical slots 1 (blue) and 2 (orange), stepped
// per color scheme; both pairs validate CVD + contrast on their surface.
const COLORS = {
  light: { aToB: '#2a78d6', bToA: '#eb6834', grid: '#e3e2de', axis: '#52514e' },
  dark: { aToB: '#3987e5', bToA: '#d95926', grid: '#333330', axis: '#c3c2b7' },
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

function DirectionCard({ title, s }: { title: string; s: DirectionSummary }) {
  return (
    <div className={'pair-card status-border-' + s.status}>
      <h3>{title}</h3>
      <dl>
        <div>
          <dt>Status</dt>
          <dd className={'status-text-' + s.status}>{s.status}</dd>
        </div>
        <div>
          <dt>{latencyAxisLabel(s.latency_source).replace(' (ms)', '')} min / avg / max</dt>
          <dd>
            {fmtLatency(s.latency.min_us)} / {fmtLatency(s.latency.avg_us)} /{' '}
            {fmtLatency(s.latency.max_us)}
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
  const [error, setError] = useState('')

  useEffect(() => {
    let cancelled = false
    const load = () =>
      Promise.all([
        apiGet<PairResponse>(`/api/v1/pairs/${encodeURIComponent(a)}/${encodeURIComponent(b)}?window=${win}`),
        apiGet<SeriesResponse>(
          `/api/v1/pairs/${encodeURIComponent(a)}/${encodeURIComponent(b)}/series?metric=${metric}&window=${win}`,
        ),
      ])
        .then(([p, s]) => {
          if (!cancelled) {
            setPair(p)
            setSeries(s)
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
        height: 220,
        series,
        scales: metric === 'loss' ? { y: { range: [0, 100] } } : {},
        axes: [
          { ...axisStyle },
          { ...axisStyle, label: axisLabel, size: 60 },
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

  return (
    <section>
      <div className="section-head">
        <h2>
          <a href="#/">Matrix</a> / {a} ⇄ {b}
        </h2>
        <span className="muted">
          {series.resolution_s >= 3600
            ? `${series.resolution_s / 3600} h buckets`
            : `${series.resolution_s / 60} min buckets`}
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
        <DirectionCard title={`${a} → ${b}`} s={pair.a_to_b} />
        <DirectionCard title={`${b} → ${a}`} s={pair.b_to_a} />
      </div>

      <div className="chart-block">
        <h3>
          <span className="swatch series-a" /> {a} → {b}
        </h3>
        <Chart options={mkOptions('aToB', axisLabel)} data={toChartData(series.a_to_b.points, metric)} />
      </div>
      <div className="chart-block">
        <h3>
          <span className="swatch series-b" /> {b} → {a}
        </h3>
        <Chart options={mkOptions('bToA', axisLabel)} data={toChartData(series.b_to_a.points, metric)} />
      </div>
    </section>
  )
}
