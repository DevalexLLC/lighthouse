import { useMemo, useState } from 'react'
import {
  WORLD_LAT_BOTTOM,
  WORLD_LAT_TOP,
  WORLD_PATH,
  WORLD_VIEW_H,
  WORLD_VIEW_W,
} from '../assets/worldPath'
import { fmtLatency, latencySourceName } from '../format'
import { directionSeverity, pairSeverity, SEVERITY_LABEL, worst, type Severity } from '../severity'
import type { MatrixCell, Site, ThresholdSettings } from '../types'

// Same transform build-world-path.mjs used for the land outline, so dots and
// coastline can never drift apart.
function project(lat: number, lon: number): { x: number; y: number } {
  return {
    x: ((lon + 180) / 360) * WORLD_VIEW_W,
    y: Math.min(((WORLD_LAT_TOP - lat) / (WORLD_LAT_TOP - WORLD_LAT_BOTTOM)) * WORLD_VIEW_H, WORLD_VIEW_H),
  }
}

// One line per unordered site pair; both directions folded into it.
interface PairLine {
  a: Site
  b: Site
  severity: Severity
  title: string
}

// Site names are unrestricted text (spaces included); NUL cannot appear in
// Postgres text, so it is the one collision-free separator.
const pairKey = (a: string, b: string) => a + '\u0000' + b

function directionText(src: string, dst: string, cell: MatrixCell | undefined, t: ThresholdSettings | null): string {
  // No cell = the direction is not configured, which is not the same
  // unknown as a configured direction gone silent (explicit stale cell).
  if (!cell) return `${src} → ${dst}: not probed`
  const sev = directionSeverity(cell, t)
  let s = `${src} → ${dst}: ${SEVERITY_LABEL[sev]}`
  if (cell.latency_us != null) {
    s += ` · ${fmtLatency(cell.latency_us)} ${latencySourceName(cell.latency_source)}`
  }
  if (cell.loss_pct != null && cell.loss_pct > 0) {
    s += ` · ${cell.loss_pct.toFixed(0)}% loss`
  }
  return s
}

export default function WorldMap({
  sites,
  cells,
  thresholds,
}: {
  sites: Site[]
  cells: MatrixCell[]
  thresholds: ThresholdSettings | null
}) {
  const [pinned, setPinned] = useState<string | null>(null)

  const { placed, unplaced, lines, siteSeverity } = useMemo(() => {
    const placed = sites.filter((s) => s.latitude != null && s.longitude != null)
    const unplaced = sites.filter((s) => s.latitude == null || s.longitude == null)
    const placedNames = new Set(placed.map((s) => s.name))
    const byName = new Map(sites.map((s) => [s.name, s]))

    const cellFor = new Map<string, MatrixCell>()
    for (const c of cells) cellFor.set(pairKey(c.src, c.dst), c)

    // Every site's severity folds all cells touching it in either direction,
    // so an unplaced site's chip is as honest as a placed site's dot. Sites
    // no cell touches stay out of the map and read back as stale below — a
    // site with no measurements must not claim OK.
    const siteSeverity = new Map<string, Severity>()
    for (const c of cells) {
      const sev = directionSeverity(c, thresholds)
      for (const name of [c.src, c.dst]) {
        const prev = siteSeverity.get(name)
        siteSeverity.set(name, prev === undefined ? sev : worst(prev, sev))
      }
    }

    const seen = new Set<string>()
    const lines: PairLine[] = []
    for (const c of cells) {
      const [x, y] = c.src < c.dst ? [c.src, c.dst] : [c.dst, c.src]
      const key = pairKey(x, y)
      if (seen.has(key) || !placedNames.has(x) || !placedNames.has(y)) continue
      seen.add(key)
      const ab = cellFor.get(pairKey(x, y))
      const ba = cellFor.get(pairKey(y, x))
      lines.push({
        a: byName.get(x)!,
        b: byName.get(y)!,
        severity: pairSeverity(ab, ba, thresholds),
        title: `${directionText(x, y, ab, thresholds)}\n${directionText(y, x, ba, thresholds)}`,
      })
    }
    return { placed, unplaced, lines, siteSeverity }
  }, [sites, cells, thresholds])

  // Sites the matrix has never measured read as stale, never as OK.
  const sevOf = (name: string): Severity => siteSeverity.get(name) ?? 'stale'

  const missingStrip = unplaced.length > 0 && (
    // Fail loud: sites without coordinates never silently vanish, and they
    // keep their live severity while off the map.
    <div className="map-missing">
      <span className="hint">
        Not on the map — set coordinates with <code>lighthouse-server site set</code>:
      </span>
      {unplaced.map((s) => {
        const sev = sevOf(s.name)
        return (
          <span key={s.name} className="chip">
            <span className={`dot swatch sev-${sev}`} />
            {s.display_name || s.name} · {SEVERITY_LABEL[sev]}
          </span>
        )
      })}
    </div>
  )

  const legend = (
    <div className="legend">
      {(['ok', 'warn', 'crit', 'down', 'stale'] as const).map((s) => (
        <span key={s} className="legend-item">
          <span className={'swatch sev-' + s} /> {SEVERITY_LABEL[s]}
        </span>
      ))}
    </div>
  )

  if (placed.length === 0) {
    return (
      <>
        <div className="map-empty">
          <p className="muted">
            No sites have map coordinates yet. Place them with{' '}
            <code>lighthouse-server site set --name &lt;site&gt; --lat &lt;deg&gt; --lon &lt;deg&gt;</code>
          </p>
        </div>
        {missingStrip}
        {unplaced.length > 0 && legend}
      </>
    )
  }

  const togglePin = (name: string) => setPinned((p) => (p === name ? null : name))

  return (
    <>
      <svg
        className={'worldmap' + (pinned ? ' has-pin' : '')}
        viewBox={`0 0 ${WORLD_VIEW_W} ${WORLD_VIEW_H}`}
        role="img"
        aria-label={`World map of ${placed.length} sites and ${lines.length} inter-site links`}
      >
        {/* Ocean click clears the pin. */}
        <rect
          className="map-ocean"
          width={WORLD_VIEW_W}
          height={WORLD_VIEW_H}
          onClick={() => setPinned(null)}
        />
        <path className="map-land" d={WORLD_PATH} />
        {lines.map((l) => {
          const p1 = project(l.a.latitude!, l.a.longitude!)
          const p2 = project(l.b.latitude!, l.b.longitude!)
          // Pacific links take the short way: when the projected span exceeds
          // half the world, draw toward a wrapped endpoint and add the same
          // curve shifted a world-width over, so the two halves meet at the
          // map edges (the svg clips the overhang).
          let shift = 0
          if (p2.x - p1.x > WORLD_VIEW_W / 2) shift = -WORLD_VIEW_W
          else if (p2.x - p1.x < -WORLD_VIEW_W / 2) shift = WORLD_VIEW_W
          const ex = p2.x + shift
          // Shallow quadratic bow (~8% of the chord, perpendicular) so lines
          // clear each other and the coastline. Cosmetic — not a route.
          const mx = (p1.x + ex) / 2
          const my = (p1.y + p2.y) / 2
          const dx = ex - p1.x
          const dy = p2.y - p1.y
          const len = Math.hypot(dx, dy) || 1
          const cx = mx - (dy / len) * len * 0.08
          const cy = my + (dx / len) * len * 0.08
          const seg = (ox: number) =>
            `M${(p1.x + ox).toFixed(1)},${p1.y.toFixed(1)} Q${(cx + ox).toFixed(1)},${cy.toFixed(1)} ${(ex + ox).toFixed(1)},${p2.y.toFixed(1)}`
          const d = shift === 0 ? seg(0) : `${seg(0)} ${seg(-shift)}`
          const involved = pinned === l.a.name || pinned === l.b.name
          return (
            <a
              key={pairKey(l.a.name, l.b.name)}
              href={`#/pair/${encodeURIComponent(l.a.name)}/${encodeURIComponent(l.b.name)}`}
              aria-label={l.title.replace('\n', '; ')}
            >
              <title>{l.title}</title>
              {/* Wide invisible twin makes thin lines clickable. */}
              <path className="map-hit" d={d} />
              <path className={`map-line sev-${l.severity}` + (involved ? ' pinned' : '')} d={d} />
            </a>
          )
        })}
        {placed.map((s) => {
          const { x, y } = project(s.latitude!, s.longitude!)
          const sev = sevOf(s.name)
          const label = s.display_name || s.name
          const title = `${label} · ${SEVERITY_LABEL[sev]}${s.location ? ` · ${s.location}` : ''}`
          // Labels flip above the dot near the bottom edge.
          const labelY = y > WORLD_VIEW_H - 18 ? y - 9 : y + 14
          return (
            <g
              key={s.name}
              className={`map-site sev-${sev}` + (pinned === s.name ? ' pinned' : '')}
              role="button"
              tabIndex={0}
              aria-pressed={pinned === s.name}
              aria-label={`${title}. Toggles highlighting this site's links.`}
              onClick={() => togglePin(s.name)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' || e.key === ' ') {
                  e.preventDefault()
                  togglePin(s.name)
                }
              }}
            >
              <title>{title}</title>
              <circle className="map-halo" cx={x} cy={y} r={4.5} />
              <circle className="map-dot" cx={x} cy={y} r={4.5} />
              <text className="map-label" x={x} y={labelY} textAnchor="middle">
                {label}
              </text>
            </g>
          )
        })}
      </svg>
      {missingStrip}
      {legend}
    </>
  )
}
