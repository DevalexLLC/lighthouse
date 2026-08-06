import type { AgentHealthBucket } from '../types'

// 48 × 30-min slots = the 24 h the agent-health endpoints serve.
export const SLOTS = 48

type SlotClass = 'ok' | 'degraded' | 'down' | 'nodata'

function slotTime(epochS: number, withZone: boolean): string {
  return new Date(epochS * 1000).toLocaleTimeString(
    [],
    withZone ? { hour: '2-digit', minute: '2-digit', timeZoneName: 'short' } : { hour: '2-digit', minute: '2-digit' },
  )
}

// slotTitle is the per-slot hover detail: the bucket's local time window
// plus its counts, so a failed block answers "when, and how badly" without
// leaving the strip. Wall-clock times are unambiguous within a 24 h window
// except across a DST transition (fall-back repeats an hour, so two buckets
// would share a label and a range can read reversed); withZone appends the
// short zone name on exactly those days.
function slotTitle(t: number, bucketS: number, withZone: boolean, b: AgentHealthBucket | undefined): string {
  const range = `${slotTime(t, withZone)}–${slotTime(t + bucketS, withZone)}`
  if (!b || b.samples === 0) return `${range} · no samples`
  return `${range} · ${b.ok} of ${b.samples} ok`
}

// One series' 24 h as a segmented bar strip (hand-rolled SVG — uPlot is for
// real charts). Slots align to bucket boundaries ending at the current
// bucket; a slot with no samples renders muted, never as success. Each slot
// carries a native tooltip over the full slot width (the visible segment is
// narrower than its slot, and thin hit targets make hover misses cheap).
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
  // A UTC-offset change inside the window means a DST transition: label
  // every slot with the zone that day so repeated wall-clock hours stay
  // distinguishable.
  const withZone =
    new Date((endS - SLOTS * bucketS) * 1000).getTimezoneOffset() !== new Date(endS * 1000).getTimezoneOffset()
  const slots: { cls: SlotClass; title: string }[] = []
  for (let i = 0; i < SLOTS; i++) {
    const t = endS - (SLOTS - i) * bucketS
    const b = byStart.get(t)
    let cls: SlotClass
    if (!b || b.samples === 0) cls = 'nodata'
    else if (b.ok === 0) cls = 'down'
    else if (b.ok < b.samples) cls = 'degraded'
    else cls = 'ok'
    slots.push({ cls, title: slotTitle(t, bucketS, withZone, b) })
  }
  return (
    <svg
      className="fleet-strip"
      viewBox={`0 0 ${SLOTS * 2} 12`}
      preserveAspectRatio="none"
      role="img"
      aria-label={label}
    >
      {slots.map((s, i) => (
        <g key={i}>
          <rect className={'fleet-strip-seg strip-' + s.cls} x={i * 2} y={1} width={1.4} height={10} rx={0.7} />
          <rect x={i * 2} y={0} width={2} height={12} fill="transparent">
            <title>{s.title}</title>
          </rect>
        </g>
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
