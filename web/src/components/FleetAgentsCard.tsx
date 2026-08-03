import { fmtAgo } from '../format'
import type { AgentHealthBucket, AgentHealthResponse, AgentInfo } from '../types'

// 48 × 30-min slots = the 24 h the /agents/health endpoint serves.
const SLOTS = 48

type SlotClass = 'ok' | 'degraded' | 'down' | 'nodata'

// One agent's 24 h as a segmented bar strip (hand-rolled SVG — uPlot is for
// real charts). Slots align to bucket boundaries ending at the current
// bucket; a slot with no samples renders muted, never as success.
function HealthStrip({
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
        <rect
          key={i}
          className={'fleet-strip-seg strip-' + cls}
          x={i * 2}
          y={1}
          width={1.4}
          height={10}
          rx={0.7}
        />
      ))}
    </svg>
  )
}

// Fleet health at a glance: one row per agent with its last update, 24 h
// probe-success strip, and uptime %. Agents absent from the health response
// have no results in the window — they show "—", never an invented 100 %.
export default function FleetAgentsCard({
  agents,
  health,
}: {
  agents: AgentInfo[]
  health: AgentHealthResponse | null
}) {
  const bucketS = health?.bucket_s ?? 1800
  // Align the slot grid to bucket boundaries; the newest (partial) bucket is
  // included so fresh failures show up within a poll cycle.
  const nowS = Date.now() / 1000
  const endS = (Math.floor(nowS / bucketS) + 1) * bucketS
  // The last slot is always the in-progress bucket — coverage judgments
  // below must only weigh the completed ones, or every agent would flicker
  // to "partial" right after each boundary (and a lone fresh sample would
  // claim a full 30 minutes).
  const currentStart = endS - bucketS
  const healthById = new Map(health?.agents.map((a) => [a.id, a.buckets]) ?? [])

  return (
    <section className="card overview-fleet">
      <div className="card-head">
        <div>
          <span className="eyebrow">Fleet</span>
          <h2>Agents</h2>
        </div>
        <a className="text-link" href="#/agents">
          View agents
        </a>
      </div>
      {agents.length === 0 ? (
        <div className="empty-state">
          <strong>No agents enrolled</strong>
          <span>Enroll an agent to start measuring.</span>
        </div>
      ) : (
        <div className="fleet-scroll">
          <table className="fleet-table">
            <thead>
              <tr>
                <th scope="col">Agent</th>
                <th scope="col">Last update</th>
                <th scope="col">24 h health</th>
                <th scope="col" className="fleet-uptime">
                  Uptime
                </th>
              </tr>
            </thead>
            <tbody>
              {agents.map((a) => {
                const buckets = healthById.get(a.id) ?? []
                const inWindow = buckets.filter((b) => b.t >= endS - SLOTS * bucketS)
                const samples = inWindow.reduce((s, b) => s + b.samples, 0)
                const ok = inWindow.reduce((s, b) => s + b.ok, 0)
                const uptime = samples > 0 ? (100 * ok) / samples : null
                // The ratio only covers buckets that have samples — an agent
                // that succeeded briefly and then went silent must not read
                // as a confident 100%. Partial coverage renders muted with
                // the measured span spelled out. Full confidence = every
                // COMPLETED slot covered; the in-progress bucket is prorated
                // into the measured hours but never decides coverage.
                const completedCovered = inWindow.filter(
                  (b) => b.samples > 0 && b.t < currentStart,
                ).length
                const currentHasData = inWindow.some(
                  (b) => b.t >= currentStart && b.samples > 0,
                )
                const coveredHours =
                  (completedCovered * bucketS) / 3600 +
                  (currentHasData ? (nowS - currentStart) / 3600 : 0)
                const partial = uptime != null && completedCovered < SLOTS - 1
                const stripLabel =
                  uptime == null
                    ? 'No probe results in the last 24 hours'
                    : partial
                      ? `Probe success ${uptime.toFixed(1)}% over the ${coveredHours.toFixed(1)} measured hours of the last 24`
                      : `24 hour probe success ${uptime.toFixed(1)}%`
                return (
                  <tr key={a.id}>
                    <td>
                      <strong>{a.site}</strong>
                      <small>{a.hostname}</small>
                    </td>
                    <td className="fleet-seen">{a.last_seen_at ? fmtAgo(a.last_seen_at) : 'never'}</td>
                    <td className="fleet-health">
                      <HealthStrip
                        buckets={inWindow}
                        bucketS={bucketS}
                        endS={endS}
                        label={stripLabel}
                      />
                    </td>
                    <td className="fleet-uptime">
                      {uptime == null ? (
                        <span title="No probe results in the last 24 hours">—</span>
                      ) : partial ? (
                        <span className="fleet-uptime-partial" title={stripLabel}>
                          {uptime.toFixed(1)}%*
                        </span>
                      ) : (
                        `${uptime.toFixed(1)}%`
                      )}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
    </section>
  )
}
