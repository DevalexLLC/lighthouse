// Natural Earth I projection + great-circle arcs for the operations map.
// The raw polynomial is identical to d3-geo's naturalEarth1Raw and MUST stay
// in lockstep with web/tools/build-map-geo.mjs, which bakes the same
// projection into assets/mapGeo.ts — nodes, arcs, and
// the committed geometry share one transform so they can never drift.
import { MAP_K, MAP_TX, MAP_TY } from './assets/mapGeo'

const RAD = Math.PI / 180

function naturalEarth1Raw(lambda: number, phi: number): [number, number] {
  const phi2 = phi * phi
  const phi4 = phi2 * phi2
  return [
    lambda *
      (0.8707 - 0.131979 * phi2 + phi4 * (-0.013791 + phi4 * (0.003971 * phi2 - 0.001529 * phi4))),
    phi * (1.007226 + phi2 * (0.015085 + phi4 * (-0.044475 + 0.028874 * phi2 - 0.005916 * phi4))),
  ]
}

// Geographic degrees → map frame pixels (1080×600, fit constants from
// the generated asset; screen y grows downward).
export function projectMap(lon: number, lat: number): { x: number; y: number } {
  const [x, y] = naturalEarth1Raw(lon * RAD, lat * RAD)
  return { x: MAP_TX + MAP_K * x, y: MAP_TY - MAP_K * y }
}

// Great-circle arc between two sites as an SVG path, resampled along the
// geodesic (spherical linear interpolation) — never a straight screen-space
// line. A segment jumping more than 180° of longitude has crossed the
// antimeridian: split into a new subpath there, the same effect as d3's
// antimeridian clipping at this stroke weight.
export interface GreatCircleGeometry {
  path: string
  seams: Array<[{ x: number; y: number }, { x: number; y: number }]>
}

export function greatCircleGeometry(
  lonA: number,
  latA: number,
  lonB: number,
  latB: number,
  samples = 64,
): GreatCircleGeometry {
  const a = toUnit(lonA, latA)
  let b = toUnit(lonB, latB)
  const cosOmega = (p: [number, number, number], q: [number, number, number]) =>
    Math.max(-1, Math.min(1, p[0] * q[0] + p[1] * q[1] + p[2] * q[2]))
  let omega = Math.acos(cosOmega(a, b))
  if (omega === 0) return { path: '', seams: [] }
  // Antipodal endpoints have no unique great circle and sin(omega)
  // underflows, scribbling garbage across the map. Nudge B ~100 m toward
  // the equator (latitude, so it works at the poles too) to pick one
  // deterministic, visually identical arc.
  if (Math.PI - omega < 1e-6) {
    b = toUnit(lonB, latB > 0 ? latB - 1e-3 : latB + 1e-3)
    omega = Math.acos(cosOmega(a, b))
  }
  const sinOmega = Math.sin(omega)

  let d = ''
  let pen: 'M' | 'L' = 'M'
  let prevLon = lonA
  let prevPoint: { x: number; y: number } | null = null
  const seams: GreatCircleGeometry['seams'] = []
  for (let i = 0; i <= samples; i++) {
    const t = i / samples
    const ka = Math.sin((1 - t) * omega) / sinOmega
    const kb = Math.sin(t * omega) / sinOmega
    const x = ka * a[0] + kb * b[0]
    const y = ka * a[1] + kb * b[1]
    const z = ka * a[2] + kb * b[2]
    const lon = Math.atan2(y, x) / RAD
    const lat = Math.atan2(z, Math.hypot(x, y)) / RAD
    const p = projectMap(lon, lat)
    if (i > 0 && Math.abs(lon - prevLon) > 180) {
      pen = 'M'
      if (prevPoint) seams.push([prevPoint, p])
    }
    prevLon = lon
    d += `${pen}${p.x.toFixed(1)},${p.y.toFixed(1)}`
    prevPoint = p
    pen = 'L'
  }
  return { path: d, seams }
}

function toUnit(lon: number, lat: number): [number, number, number] {
  const l = lon * RAD
  const p = lat * RAD
  return [Math.cos(p) * Math.cos(l), Math.cos(p) * Math.sin(l), Math.sin(p)]
}
