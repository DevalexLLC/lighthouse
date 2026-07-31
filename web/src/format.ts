// Human formatting for wire microseconds and timestamps.

// Value/unit split so headlines can typeset the unit smaller than the number.
export function fmtLatencyParts(us: number | null | undefined): { value: string; unit: string } {
  if (us == null) return { value: '—', unit: '' }
  const rounded = Math.round(us)
  if (rounded < 1000) return { value: `${rounded}`, unit: 'µs' }
  if (us < 1_000_000) return { value: (us / 1000).toFixed(us < 10_000 ? 2 : 1), unit: 'ms' }
  return { value: (us / 1_000_000).toFixed(2), unit: 's' }
}

export function fmtLatency(us: number | null | undefined): string {
  const p = fmtLatencyParts(us)
  return p.unit ? `${p.value} ${p.unit}` : p.value
}

type LatencyUnit = 'µs' | 'ms' | 's'

function latencyUnit(values: (number | null | undefined)[]): LatencyUnit {
  const max = Math.max(0, ...values.filter((v): v is number => v != null).map(Math.abs))
  if (max < 1000) return 'µs'
  if (max < 1_000_000) return 'ms'
  return 's'
}

function fmtLatencyInUnit(us: number | null | undefined, unit: LatencyUnit): string {
  if (us == null) return '—'
  if (unit === 'µs') return Math.round(us).toString()
  if (unit === 'ms') {
    const ms = us / 1000
    return ms.toFixed(ms < 1 ? 3 : ms < 10 ? 2 : 1)
  }
  return (us / 1_000_000).toFixed(2)
}

// Related values use one unit so min/max and percentile rows scan cleanly.
export function fmtLatencyGroup(values: (number | null | undefined)[]): string {
  if (values.every((v) => v == null)) return '—'
  const unit = latencyUnit(values)
  return `${values.map((v) => fmtLatencyInUnit(v, unit)).join(' / ')} ${unit}`
}

export function fmtTime(iso: string | null | undefined): string {
  if (!iso) return '—'
  return new Date(iso).toLocaleString()
}

// Compact relative time for "last seen" style fields.
export function fmtAgo(iso: string | null | undefined): string {
  if (!iso) return 'never'
  const s = Math.max(0, Math.round((Date.now() - new Date(iso).getTime()) / 1000))
  if (s < 60) return `${s}s ago`
  if (s < 3600) return `${Math.round(s / 60)}m ago`
  if (s < 86400) return `${Math.round(s / 3600)}h ago`
  return `${Math.round(s / 86400)}d ago`
}

// Human name for the API's latency_source (what "latency" measures).
export function latencySourceName(source: string): string {
  switch (source) {
    case 'rtt':
      return 'RTT'
    case 'tcp_connect':
      return 'TCP connect'
    case 'tls_handshake':
      return 'TLS handshake'
    case 'ttfb':
      return 'TTFB'
    case 'total':
      return 'Total time'
    default:
      return 'Latency'
  }
}

// Axis label for the latency metric, from the API's latency_source.
export function latencyAxisLabel(source: string): string {
  return `${latencySourceName(source)} (ms)`
}
