// Human formatting for wire microseconds and timestamps.

export function fmtLatency(us: number | null | undefined): string {
  if (us == null) return '—'
  if (us < 1000) return `${us} µs`
  if (us < 1_000_000) return `${(us / 1000).toFixed(us < 10_000 ? 2 : 1)} ms`
  return `${(us / 1_000_000).toFixed(2)} s`
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

// Axis label for the latency metric, from the API's latency_source.
export function latencyAxisLabel(source: string): string {
  switch (source) {
    case 'rtt':
      return 'RTT (ms)'
    case 'tcp_connect':
      return 'TCP connect (ms)'
    case 'tls_handshake':
      return 'TLS handshake (ms)'
    case 'ttfb':
      return 'TTFB (ms)'
    case 'total':
      return 'Total time (ms)'
    default:
      return 'Latency (ms)'
  }
}
