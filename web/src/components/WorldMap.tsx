import { useMemo, useState } from 'react'
import {
  MAP_COUNTRIES_PATH,
  MAP_GRATICULE_PATH,
  MAP_SPHERE_PATH,
  MAP_VIEW_H,
  MAP_VIEW_W,
} from '../assets/mapGeo'
import { fmtLatency, latencySourceName } from '../format'
import { greatCircleGeometry, projectMap, type GreatCircleGeometry } from '../geo'
import { directionSeverity, pairSeverity, SEVERITY_LABEL, worst, type Severity } from '../severity'
import type { MatrixCell, Site, ThresholdSettings } from '../types'

// One line per unordered site pair; both directions folded into it. The
// per-direction severities survive the fold (null = that direction is not
// configured) so photons can depict only real, working traffic.
interface PairLine {
  a: Site
  b: Site
  severity: Severity
  aToB: Severity | null
  bToA: Severity | null
  title: string
  geometry: GreatCircleGeometry
}

// A direction demonstrably carries traffic: not down, not silent, and
// actually configured. Only these earn a photon.
const flowing = (s: Severity | null): boolean => s === 'ok' || s === 'warn' || s === 'crit'

// Site names are unrestricted text (spaces included); NUL cannot appear in
// Postgres text, so it is the one collision-free separator.
const pairKey = (a: string, b: string) => a + '\u0000' + b

interface LabelPlacement {
  x: number
  y: number
  anchor: 'start' | 'middle' | 'end'
}

function placeSiteLabels(sites: Site[]): Map<string, LabelPlacement> {
  const placed = new Map<string, LabelPlacement>()
  const occupied: Array<{ left: number; right: number; top: number; bottom: number }> = sites.map((site) => {
    const point = projectMap(site.longitude!, site.latitude!)
    return { left: point.x - 7, right: point.x + 7, top: point.y - 7, bottom: point.y + 7 }
  })
  const candidates: Array<{ dx: number; dy: number; anchor: LabelPlacement['anchor'] }> = [
    { dx: 0, dy: -12, anchor: 'middle' },
    { dx: 10, dy: -5, anchor: 'start' },
    { dx: -10, dy: -5, anchor: 'end' },
    { dx: 0, dy: 17, anchor: 'middle' },
    { dx: 10, dy: 14, anchor: 'start' },
    { dx: -10, dy: 14, anchor: 'end' },
  ]

  for (const site of sites) {
    const point = projectMap(site.longitude!, site.latitude!)
    const width = Math.max(18, site.name.length * 7)
    let selected = candidates[0]
    for (const candidate of candidates) {
      const x = point.x + candidate.dx
      const y = point.y + candidate.dy
      const left = candidate.anchor === 'start' ? x : candidate.anchor === 'end' ? x - width : x - width / 2
      const rect = { left: left - 3, right: left + width + 3, top: y - 10, bottom: y + 3 }
      const inBounds = rect.left >= 8 && rect.right <= MAP_VIEW_W - 8 && rect.top >= 8 && rect.bottom <= MAP_VIEW_H - 8
      const overlaps = occupied.some(
        (other) => rect.left < other.right && rect.right > other.left && rect.top < other.bottom && rect.bottom > other.top,
      )
      if (inBounds && !overlaps) {
        selected = candidate
        occupied.push(rect)
        break
      }
    }
    placed.set(site.name, {
      x: point.x + selected.dx,
      y: point.y + selected.dy,
      anchor: selected.anchor,
    })
  }
  return placed
}

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
  const [hovered, setHovered] = useState<string | null>(null)

  const { placed, unplaced, lines, siteSeverity, siteSummary } = useMemo(() => {
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
    const siteSummary = new Map<string, { degree: number; bestLatencyUs: number | null }>()
    for (const site of sites) siteSummary.set(site.name, { degree: 0, bestLatencyUs: null })
    for (const c of cells) {
      const [x, y] = c.src < c.dst ? [c.src, c.dst] : [c.dst, c.src]
      const key = pairKey(x, y)
      if (seen.has(key)) continue
      seen.add(key)
      const ab = cellFor.get(pairKey(x, y))
      const ba = cellFor.get(pairKey(y, x))
      // The inspector's link count and best latency cover every monitored
      // pair — a placed site probing an unplaced peer still has that link.
      // Only drawing the carrier needs both endpoints on the map.
      const liveLatencies = [ab, ba]
        .filter((cell) => cell?.status === 'ok' || cell?.status === 'degraded')
        .map((cell) => cell?.latency_us)
        .filter((latency): latency is number => latency != null)
      for (const name of [x, y]) {
        const summary = siteSummary.get(name)
        if (!summary) continue
        summary.degree++
        if (liveLatencies.length > 0) {
          const best = Math.min(...liveLatencies)
          summary.bestLatencyUs = summary.bestLatencyUs == null ? best : Math.min(summary.bestLatencyUs, best)
        }
      }
      if (!placedNames.has(x) || !placedNames.has(y)) continue
      lines.push({
        a: byName.get(x)!,
        b: byName.get(y)!,
        severity: pairSeverity(ab, ba, thresholds),
        aToB: ab ? directionSeverity(ab, thresholds) : null,
        bToA: ba ? directionSeverity(ba, thresholds) : null,
        title: `${directionText(x, y, ab, thresholds)}\n${directionText(y, x, ba, thresholds)}`,
        geometry: greatCircleGeometry(
          byName.get(x)!.longitude!,
          byName.get(x)!.latitude!,
          byName.get(y)!.longitude!,
          byName.get(y)!.latitude!,
        ),
      })
    }
    return { placed, unplaced, lines, siteSeverity, siteSummary }
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
    <div className="map-legend" aria-label="Map status legend">
      {(['ok', 'warn', 'down', 'stale'] as const).map((s) => (
        <span key={s} className="legend-item">
          <span className={'map-legend-rule sev-' + s} /> {SEVERITY_LABEL[s]}
        </span>
      ))}
      <span className="map-legend-hint">Select a site to isolate links</span>
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
      </>
    )
  }

  const togglePin = (name: string) => setPinned((p) => (p === name ? null : name))
  const hoveredSite = placed.find((site) => site.name === hovered) ?? null
  const hoveredPoint = hoveredSite
    ? projectMap(hoveredSite.longitude!, hoveredSite.latitude!)
    : null
  const hoveredSummary = hoveredSite ? siteSummary.get(hoveredSite.name) : null
  const labelPlacements = placeSiteLabels(placed)
  const edgeCrossingCount = lines.filter((line) => line.geometry.seams.length > 0).length

  return (
    <>
      <div className="worldmap-shell">
        <svg
          className={'worldmap' + (pinned ? ' has-pin' : '')}
          viewBox={`0 0 ${MAP_VIEW_W} ${MAP_VIEW_H}`}
          role="img"
          aria-label={`World map of ${placed.length} sites and ${lines.length} inter-site links`}
        >
          <rect className="map-ocean" width={MAP_VIEW_W} height={MAP_VIEW_H} onClick={() => setPinned(null)} />
          <path className="map-sphere" d={MAP_SPHERE_PATH} onClick={() => setPinned(null)} />
          <path className="map-graticule" d={MAP_GRATICULE_PATH} />
          <path className="map-land" d={MAP_COUNTRIES_PATH} />
          {lines.map((l) => {
            const p1 = projectMap(l.a.longitude!, l.a.latitude!)
            const p2 = projectMap(l.b.longitude!, l.b.latitude!)
            const geometry = l.geometry
            const d = geometry.path
            const involved = pinned === l.a.name || pinned === l.b.name
            const key = pairKey(l.a.name, l.b.name)
            const fwd = flowing(l.aToB)
            const rev = flowing(l.bToA)
            const chord = Math.hypot(p2.x - p1.x, p2.y - p1.y)
            const dur = Math.min(10, Math.max(4, chord / 70))
            let h = 0
            for (let i = 0; i < key.length; i++) h = (h * 31 + key.charCodeAt(i)) % 997
            const delayA = -((h % Math.round(dur * 10)) / 10)
            const delayB = -(((h * 7 + 383) % Math.round(dur * 10)) / 10)
            return (
              <a
                key={key}
                href={`#/pair/${encodeURIComponent(l.a.name)}/${encodeURIComponent(l.b.name)}`}
                aria-label={l.title.replace('\n', '; ')}
              >
                <title>{l.title}</title>
                <path className="map-hit" d={d} />
                <path className={`map-line sev-${l.severity}` + (involved ? ' pinned' : '')} d={d} />
                {geometry.seams.map(([west, east], index) => (
                  <g className={`map-seam sev-${l.severity}`} key={index} aria-hidden="true">
                    <circle cx={west.x} cy={west.y} r={2.6} />
                    <circle cx={east.x} cy={east.y} r={2.6} />
                  </g>
                ))}
                {(fwd || rev) && (
                  <g className={'map-flow-signals' + (involved ? ' pinned' : '')} aria-hidden="true">
                    {fwd && (
                      <path
                        className={`map-flow-pulse sev-${l.aToB}`}
                        d={d}
                        style={{ animationDuration: `${dur}s`, animationDelay: `${delayA}s` }}
                      />
                    )}
                    {rev && (
                      <path
                        className={`map-flow-pulse return sev-${l.bToA}`}
                        d={d}
                        style={{ animationDuration: `${dur}s`, animationDelay: `${delayB}s` }}
                      />
                    )}
                  </g>
                )}
              </a>
            )
          })}
          {placed.map((s) => {
            const { x, y } = projectMap(s.longitude!, s.latitude!)
            const sev = sevOf(s.name)
            const label = s.name.toUpperCase()
            const labelPlacement = labelPlacements.get(s.name)!
            const title = `${s.display_name || s.name} · ${SEVERITY_LABEL[sev]}${s.location ? ` · ${s.location}` : ''}`
            return (
              <g
                key={s.name}
                className={`map-site sev-${sev}` + (pinned === s.name ? ' pinned' : '')}
                role="button"
                tabIndex={0}
                aria-pressed={pinned === s.name}
                aria-label={`${title}. Toggles highlighting this site's links.`}
                onClick={() => togglePin(s.name)}
                onMouseEnter={() => setHovered(s.name)}
                onMouseLeave={() => setHovered(null)}
                onFocus={() => setHovered(s.name)}
                onBlur={() => setHovered(null)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' || e.key === ' ') {
                    e.preventDefault()
                    togglePin(s.name)
                  }
                }}
              >
                <title>{title}</title>
                <circle className="map-site-hit" cx={x} cy={y} r={12} />
                <circle className="map-halo" cx={x} cy={y} r={4} />
                <circle className="map-dot" cx={x} cy={y} r={3.5} />
                {pinned === s.name && <circle className="map-selection" cx={x} cy={y} r={10} />}
                <text
                  className="map-label"
                  x={labelPlacement.x}
                  y={labelPlacement.y}
                  textAnchor={labelPlacement.anchor}
                >
                  {label}
                </text>
              </g>
            )
          })}
        </svg>
        {legend}
        {edgeCrossingCount > 0 && (
          <span className="map-seam-note">Paired edge dots continue across the Pacific</span>
        )}
        {hoveredSite && hoveredPoint && hoveredSummary && (
          <div
            className={'map-tip' + (hoveredPoint.x > MAP_VIEW_W * 0.78 ? ' map-tip-left' : '')}
            style={{
              left: `${(hoveredPoint.x / MAP_VIEW_W) * 100}%`,
              top: `${(hoveredPoint.y / MAP_VIEW_H) * 100}%`,
            }}
            role="status"
          >
            <b>{hoveredSite.name.toUpperCase()}</b> ·{' '}
            {hoveredSite.location || hoveredSite.display_name || hoveredSite.name}
            <br />
            {SEVERITY_LABEL[sevOf(hoveredSite.name)].toUpperCase()} ·{' '}
            {hoveredSummary.degree} {hoveredSummary.degree === 1 ? 'link' : 'links'} ·{' '}
            {hoveredSummary.bestLatencyUs == null ? '—' : fmtLatency(hoveredSummary.bestLatencyUs)}
          </div>
        )}
      </div>
      {missingStrip}
    </>
  )
}
