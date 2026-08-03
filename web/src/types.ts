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
  // null until an operator places the site (`lighthouse-server site set`);
  // 0 is a real coordinate, so never truthiness-check these.
  latitude: number | null
  longitude: number | null
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
  enrolled_at: string
  config_hash: string
  cert_not_after: string | null
  cert_revoked_at: string | null
  offline: boolean
  probes_failing: number
  probes_total: number
  dropped_results: number
  last_dropped_at: string | null
}

export interface AgentsResponse {
  agents: AgentInfo[]
}

// GET /api/v1/agents/health — 24h of per-agent probe success counts in
// 30-min buckets. Buckets are sparse and agents with no results in the
// window are absent: join on id against /api/v1/agents and render missing
// data honestly (never an invented 100%).
export interface AgentHealthBucket {
  t: number // bucket start, UTC epoch seconds
  samples: number
  ok: number
}

export interface AgentHealth {
  id: string
  buckets: AgentHealthBucket[]
}

export interface AgentHealthResponse {
  window: string
  bucket_s: number
  agents: AgentHealth[]
}

export type CellStatus = 'ok' | 'degraded' | 'down' | 'stale'

export interface MatrixProbe {
  type: string
  status: string
  latency_us: number | null
  latency_source: string
  loss_pct: number | null
  as_of: string
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

// Shared dashboard thresholds (GET/PUT /api/v1/settings). Latency is wire
// microseconds; the settings form converts to ms at the edge.
export interface ThresholdSettings {
  latency_warn_us: number
  latency_crit_us: number
  loss_warn_pct: number
  loss_crit_pct: number
}

export interface SettingsResponse {
  thresholds: ThresholdSettings
  updated_at: string
  updated_by: string
}

// Which table served a windowed query. Raw windows (24h/7d) have no
// percentile fields; hourly/daily (30d+) do.
export type SeriesSource = 'raw' | 'hourly' | 'daily'

export interface LatencySummary {
  min_us: number | null
  avg_us: number | null
  max_us: number | null
  // Present only when source != 'raw' (server omits the keys otherwise).
  p50_us?: number | null
  p95_us?: number | null
  p99_us?: number | null
}

export interface DirectionSummary {
  status: CellStatus
  last_ok_at: string | null
  latency: LatencySummary
  latency_source: string
  loss_pct: number | null
  samples: number
  jitter_avg_us: number | null
  tcp_connect_avg_us: number | null
  tls_handshake_avg_us: number | null
  checks: MatrixProbe[]
}

export interface PairResponse {
  a: string
  b: string
  window: string
  source: SeriesSource
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
  // Present only when source != 'raw' (server omits the keys otherwise).
  p50_us?: number | null
  p95_us?: number | null
  p99_us?: number | null
}

export interface SeriesResponse {
  metric: 'latency' | 'loss'
  window: string
  resolution_s: number
  source: SeriesSource
  latency_source: string
  a_to_b: { latency_source: string; points: SeriesPoint[] }
  b_to_a: { latency_source: string; points: SeriesPoint[] }
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

// --- Probe-workload config (/api/v1/config/*). Reads any session; writes
// admin-only. Cadence fields are wire integer milliseconds; the probes form
// converts to seconds at the edge like the thresholds form does µs↔ms.

export const PROBE_TYPES = ['icmp', 'tcp', 'tls', 'http', 'dns', 'traceroute'] as const
export type ProbeTypeName = (typeof PROBE_TYPES)[number]

export interface TargetConfig {
  id: string
  kind: 'agent' | 'external'
  name: string
  address?: string
  port?: number
  url?: string
  probe_count: number
  created_at: string
}

export interface TargetsConfigResponse {
  targets: TargetConfig[]
}

export interface MeshConfig {
  id: string
  name: string
  sites: string[]
  probe_count: number
}

export interface MeshesConfigResponse {
  meshes: MeshConfig[]
}

export interface ProbeConfig {
  id: string
  site?: string
  target?: string
  mesh?: string
  type: string
  interval_ms: number
  timeout_ms: number
  train_count: number
  train_spacing_ms: number
  params: Record<string, string>
  enabled: boolean
  created_at: string
  updated_at: string
  updated_by?: string
}

export interface ProbesConfigResponse {
  probes: ProbeConfig[]
}

// GET /api/v1/config/probe-types — the server-side param registry. The
// probe form renders its param fields from this, so the set of accepted
// keys has exactly one source of truth (internal/server/probeadmin).
export interface ParamSpec {
  key: string
  hint: string
  kind: 'string' | 'port' | 'bool' | 'enum' | 'status'
  enum?: string[]
  required_mesh?: boolean
  required_direct?: boolean
  mesh_only?: boolean
}

export interface ProbeTypeInfo {
  type: string
  params: ParamSpec[]
}

export interface ProbeTypesResponse {
  types: ProbeTypeInfo[]
}
