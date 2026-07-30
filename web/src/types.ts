// Shapes returned by /api/v1/* (the contract implemented by
// internal/server/httpapi).

export interface User {
  username: string
  role: 'admin' | 'viewer'
}

export interface LoginResponse {
  user: User
  csrf_token: string
}

export interface Site {
  name: string
  display_name: string
  location: string
}

export interface SitesResponse {
  sites: Site[]
}

export interface AgentInfo {
  id: string
  site: string
  hostname: string
  probe_address: string
  version: string
  last_seen_at: string | null
}

export interface AgentsResponse {
  agents: AgentInfo[]
}

export type CellStatus = 'ok' | 'degraded' | 'down' | 'stale'

export interface MatrixProbe {
  type: string
  status: string
  latency_us: number | null
}

export interface MatrixCell {
  src: string
  dst: string
  status: CellStatus
  latency_us: number | null
  latency_source: string
  loss_pct: number | null
  as_of: string
  probes: MatrixProbe[]
}

export interface MatrixResponse {
  sites: Site[]
  cells: MatrixCell[]
  horizon_s: number
}

export interface LatencySummary {
  min_us: number | null
  avg_us: number | null
  max_us: number | null
}

export interface DirectionSummary {
  status: CellStatus
  last_ok_at: string | null
  latency: LatencySummary
  latency_source: string
  loss_pct: number | null
  samples: number
}

export interface PairResponse {
  a: string
  b: string
  window: string
  a_to_b: DirectionSummary
  b_to_a: DirectionSummary
}

export interface SeriesPoint {
  t: number // UTC epoch seconds (bucket start)
  min_us: number | null
  avg_us: number | null
  max_us: number | null
  loss_pct: number | null
  samples: number
  failures: number
}

export interface SeriesResponse {
  metric: 'latency' | 'loss'
  window: string
  resolution_s: number
  latency_source: string
  a_to_b: { points: SeriesPoint[] }
  b_to_a: { points: SeriesPoint[] }
}

export interface OutageEvent {
  id: string
  kind: 'probe_failing' | 'agent_offline'
  agent: string
  src_site: string
  dst_site: string | null
  target: string | null
  probe_type: string | null
  opened_at: string
  closed_at: string | null
  error: string | null
}

export interface OutagesResponse {
  window: string
  outages: OutageEvent[]
}

export interface Hop {
  ttl: number
  addrs: string[]
  rtt_us: number[]
}

export interface PathEvent {
  id: string
  time: string
  agent: string
  src_site: string
  dst_site: string | null
  target: string | null
  old_path_hash: string
  new_path_hash: string
  old_hops: Hop[]
  new_hops: Hop[]
}

export interface PathEventsResponse {
  window: string
  events: PathEvent[]
}

export interface CurrentPath {
  agent: string
  updated_at: string
  dest_reached: boolean
  path_hash: string
  hops: Hop[]
}

export interface TracerouteResponse {
  a: string
  b: string
  a_to_b: { paths: CurrentPath[] }
  b_to_a: { paths: CurrentPath[] }
}

export const WINDOWS = ['24h', '7d', '30d', '90d', '365d'] as const
export type Window = (typeof WINDOWS)[number]
