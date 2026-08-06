import type { AgentHealthBucket } from '../types'

// 48 × 30-min slots = the 24 h the agent-health endpoints serve.
export const SLOTS = 48

type SlotClass = 'ok' | 'degraded' | 'down' | 'nodata'

// One series' 24 h as a segmented bar strip (hand-rolled SVG — uPlot is for
// real charts). Slots align to bucket boundaries ending at the current
// bucket; a slot with no samples renders muted, never as success.
export default function HealthStrip({
  buckets,
  bucketS,
  endS,
  label,
}: {
  buckets: AgentHealthBucket[]
  bucketS: number
  endS: number
  label: string
}) {
  const byStart = new Map(buckets.map((b) => [b.t, b]))
  const slots: SlotClass[] = []
  for (let i = 0; i < SLOTS; i++) {
    const t = endS - (SLOTS - i) * bucketS
    const b = byStart.get(t)
    if (!b || b.samples === 0) slots.push('nodata')
    else if (b.ok === 0) slots.push('down')
    else if (b.ok < b.samples) slots.push('degraded')
    else slots.push('ok')
  }
  return (
    <svg
      className="fleet-strip"
      viewBox={`0 0 ${SLOTS * 2} 12`}
      preserveAspectRatio="none"
      role="img"
      aria-label={label}
    >
      {slots.map((cls, i) => (
        <rect key={i} className={'fleet-strip-seg strip-' + cls} x={i * 2} y={1} width={1.4} height={10} rx={0.7} />
      ))}
    </svg>
  )
}

export interface StripStats {
  inWindow: AgentHealthBucket[]
  endS: number
  uptime: number | null
  partial: boolean
  coveredHours: number
  stripLabel: string
}

// stripStats is the strip's shared coverage math — the fleet card and the
// per-probe detail must not drift on what "uptime" means. The ratio only
// covers buckets that have samples: a series that succeeded briefly and
// then went silent must not read as a confident 100%. Partial coverage
// renders muted with the measured span spelled out. Full confidence =
// every COMPLETED slot covered; the last slot is always the in-progress
// bucket, which is prorated into the measured hours but never decides
// coverage (or everything would flicker to "partial" right after each
// bucket boundary, and a lone fresh sample would claim a full 30 minutes).
export function stripStats(buckets: AgentHealthBucket[], bucketS: number, nowS: number): StripStats {
  // Align the slot grid to bucket boundaries; the newest (partial) bucket
  // is included so fresh failures show up within a poll cycle.
  const endS = (Math.floor(nowS / bucketS) + 1) * bucketS
  const currentStart = endS - bucketS
  const inWindow = buckets.filter((b) => b.t >= endS - SLOTS * bucketS)
  const samples = inWindow.reduce((s, b) => s + b.samples, 0)
  const ok = inWindow.reduce((s, b) => s + b.ok, 0)
  const uptime = samples > 0 ? (100 * ok) / samples : null
  const completedCovered = inWindow.filter((b) => b.samples > 0 && b.t < currentStart).length
  const currentHasData = inWindow.some((b) => b.t >= currentStart && b.samples > 0)
  const coveredHours = (completedCovered * bucketS) / 3600 + (currentHasData ? (nowS - currentStart) / 3600 : 0)
  const partial = uptime != null && completedCovered < SLOTS - 1
  const stripLabel =
    uptime == null
      ? 'No probe results in the last 24 hours'
      : partial
        ? `Probe success ${uptime.toFixed(1)}% over the ${coveredHours.toFixed(1)} measured hours of the last 24`
        : `24 hour probe success ${uptime.toFixed(1)}%`
  return { inWindow, endS, uptime, partial, coveredHours, stripLabel }
}

// UptimeValue renders the uptime figure that accompanies a strip: an
// em-dash for no data (never an invented 100%), and a muted asterisked
// figure when coverage is partial, with the measured span in the title.
export function UptimeValue({ uptime, partial, stripLabel }: Pick<StripStats, 'uptime' | 'partial' | 'stripLabel'>) {
  if (uptime == null) return <span title={stripLabel}>—</span>
  if (partial)
    return (
      <span className="fleet-uptime-partial" title={stripLabel}>
        {uptime.toFixed(1)}%*
      </span>
    )
  return <>{uptime.toFixed(1)}%</>
}
