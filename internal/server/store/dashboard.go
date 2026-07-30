package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Dashboard read queries (M3). Everything here computes from the raw
// probe_results hypertable; continuous aggregates take over in M5.
//
// latencyExpr is the per-row latency estimate: real RTT when a prober
// measures it (ICMP, M4+), otherwise the purest available network timing.
// The source column is reported alongside so the UI can label the axis
// honestly instead of passing TCP-connect time off as RTT.
const latencyExpr = `COALESCE(rtt_avg_us, tcp_connect_us, tls_handshake_us, ttfb_us, total_us)`

const latencySourceExpr = `CASE
	WHEN rtt_avg_us       IS NOT NULL THEN 'rtt'
	WHEN tcp_connect_us   IS NOT NULL THEN 'tcp_connect'
	WHEN tls_handshake_us IS NOT NULL THEN 'tls_handshake'
	WHEN ttfb_us          IS NOT NULL THEN 'ttfb'
	WHEN total_us         IS NOT NULL THEN 'total'
	ELSE '' END`

// SiteInfo is a sites row as shown by the dashboard.
type SiteInfo struct {
	ID          uuid.UUID
	Name        string
	DisplayName string
	Location    string
}

// AgentListInfo is an agents row joined with its site name.
type AgentListInfo struct {
	ID           uuid.UUID
	Site         string
	Hostname     string
	ProbeAddress string
	Version      string
	LastSeenAt   *time.Time
}

// MatrixRow is the latest result of one (agent, agent-target, probe type)
// series, mapped to its ordered site pair.
type MatrixRow struct {
	SrcSite       string
	DstSite       string
	ProbeType     int16
	Status        int16
	Time          time.Time
	LatencyUS     *int64
	LatencySource string
	LossPct       *float32
}

// SitePair is an ordered src→dst site pair that probe configuration says
// should be producing results.
type SitePair struct {
	Src string
	Dst string
}

// SiteEndpoints are the probe-series endpoints belonging to one site: its
// agents (result senders) and those agents' targets (result destinations).
type SiteEndpoints struct {
	SiteInfo
	AgentIDs  []uuid.UUID
	TargetIDs []uuid.UUID
}

// SeriesBucket is one time_bucket of a directional pair series.
type SeriesBucket struct {
	Bucket   time.Time
	MinUS    *float64
	AvgUS    *float64
	MaxUS    *float64
	LossPct  *float64
	Samples  int64
	Failures int64
}

// PairSummaryRow aggregates one direction of a site pair over a window.
type PairSummaryRow struct {
	MinUS         *float64
	AvgUS         *float64
	MaxUS         *float64
	LossPct       *float64
	Samples       int64
	LastOKAt      *time.Time
	LatencySource string
}

// ListSites returns all sites ordered by name.
func (s *Store) ListSites(ctx context.Context) ([]SiteInfo, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, display_name, location FROM sites ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list sites: %w", err)
	}
	defer rows.Close()
	var out []SiteInfo
	for rows.Next() {
		var si SiteInfo
		if err := rows.Scan(&si.ID, &si.Name, &si.DisplayName, &si.Location); err != nil {
			return nil, fmt.Errorf("list sites: %w", err)
		}
		out = append(out, si)
	}
	return out, rows.Err()
}

// ListAgents returns all agents with their site names.
func (s *Store) ListAgents(ctx context.Context) ([]AgentListInfo, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT a.id, s.name, a.hostname, a.probe_address, a.version, a.last_seen_at
		   FROM agents a JOIN sites s ON s.id = a.site_id
		  ORDER BY s.name, a.hostname`)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	defer rows.Close()
	var out []AgentListInfo
	for rows.Next() {
		var ai AgentListInfo
		if err := rows.Scan(&ai.ID, &ai.Site, &ai.Hostname, &ai.ProbeAddress, &ai.Version, &ai.LastSeenAt); err != nil {
			return nil, fmt.Errorf("list agents: %w", err)
		}
		out = append(out, ai)
	}
	return out, rows.Err()
}

// MatrixLatest returns the latest result per (agent, agent-target, probe
// type) series within the staleness horizon, mapped to ordered site pairs.
// External targets have no destination site and are excluded by the join.
func (s *Store) MatrixLatest(ctx context.Context, horizon time.Duration) ([]MatrixRow, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(
		`WITH latest AS (
		    SELECT DISTINCT ON (agent_id, target_id, probe_type)
		           agent_id, target_id, probe_type, time, status, loss_pct,
		           %s AS latency_us, %s AS latency_source
		      FROM probe_results
		     WHERE time > now() - $1::interval
		     ORDER BY agent_id, target_id, probe_type, time DESC
		)
		SELECT ss.name, ds.name, l.probe_type, l.status, l.time,
		       l.latency_us, l.latency_source, l.loss_pct
		  FROM latest l
		  JOIN agents sa ON sa.id = l.agent_id
		  JOIN sites  ss ON ss.id = sa.site_id
		  JOIN targets t ON t.id = l.target_id AND t.agent_id IS NOT NULL
		  JOIN agents da ON da.id = t.agent_id
		  JOIN sites  ds ON ds.id = da.site_id`, latencyExpr, latencySourceExpr),
		horizon)
	if err != nil {
		return nil, fmt.Errorf("matrix latest: %w", err)
	}
	defer rows.Close()
	var out []MatrixRow
	for rows.Next() {
		var mr MatrixRow
		if err := rows.Scan(&mr.SrcSite, &mr.DstSite, &mr.ProbeType, &mr.Status, &mr.Time,
			&mr.LatencyUS, &mr.LatencySource, &mr.LossPct); err != nil {
			return nil, fmt.Errorf("matrix latest: %w", err)
		}
		out = append(out, mr)
	}
	return out, rows.Err()
}

// ExpectedPairs returns the ordered site pairs that enabled probe configs
// should be producing results for — mesh templates expanded over member
// pairs plus direct probes whose target is an agent. Pairs present here but
// absent from MatrixLatest render as stale.
func (s *Store) ExpectedPairs(ctx context.Context) ([]SitePair, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT s1.name, s2.name
		   FROM probe_configs pc
		   JOIN mesh_members m1 ON m1.mesh_id = pc.mesh_id
		   JOIN mesh_members m2 ON m2.mesh_id = pc.mesh_id AND m2.site_id <> m1.site_id
		   JOIN sites s1 ON s1.id = m1.site_id
		   JOIN sites s2 ON s2.id = m2.site_id
		  WHERE pc.enabled
		 UNION
		 SELECT DISTINCT s1.name, s2.name
		   FROM probe_configs pc
		   JOIN sites s1 ON s1.id = pc.site_id
		   JOIN targets t ON t.id = pc.target_id AND t.agent_id IS NOT NULL
		   JOIN agents da ON da.id = t.agent_id
		   JOIN sites s2 ON s2.id = da.site_id
		  WHERE pc.enabled AND s1.id <> s2.id`)
	if err != nil {
		return nil, fmt.Errorf("expected pairs: %w", err)
	}
	defer rows.Close()
	var out []SitePair
	for rows.Next() {
		var p SitePair
		if err := rows.Scan(&p.Src, &p.Dst); err != nil {
			return nil, fmt.Errorf("expected pairs: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SiteEndpoints resolves a site name to its agents and their agent-kind
// targets, or (nil, nil) when the site does not exist. Resolving IDs first
// lets the hypertable scans hit the (agent_id, target_id, ...) index with
// no joins.
func (s *Store) SiteEndpoints(ctx context.Context, siteName string) (*SiteEndpoints, error) {
	var ep SiteEndpoints
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, display_name, location FROM sites WHERE name = $1`, siteName).
		Scan(&ep.ID, &ep.Name, &ep.DisplayName, &ep.Location)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("site %q: %w", siteName, err)
	}
	rows, err := s.pool.Query(ctx,
		`SELECT a.id, t.id
		   FROM agents a
		   JOIN targets t ON t.agent_id = a.id
		  WHERE a.site_id = $1`, ep.ID)
	if err != nil {
		return nil, fmt.Errorf("site %q endpoints: %w", siteName, err)
	}
	defer rows.Close()
	for rows.Next() {
		var agentID, targetID uuid.UUID
		if err := rows.Scan(&agentID, &targetID); err != nil {
			return nil, fmt.Errorf("site %q endpoints: %w", siteName, err)
		}
		ep.AgentIDs = append(ep.AgentIDs, agentID)
		ep.TargetIDs = append(ep.TargetIDs, targetID)
	}
	return &ep, rows.Err()
}

// PairSeries buckets one direction (srcAgents → dstTargets) over the window.
func (s *Store) PairSeries(ctx context.Context, srcAgents, dstTargets []uuid.UUID, bucket, window time.Duration) ([]SeriesBucket, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(
		`SELECT time_bucket($1::interval, time) AS bucket,
		        min(%[1]s)::float8, avg(%[1]s)::float8, max(%[1]s)::float8,
		        100.0 * (1 - sum(received)::float8 / NULLIF(sum(sent), 0)),
		        count(*),
		        count(*) FILTER (WHERE status <> 1)
		   FROM probe_results
		  WHERE agent_id = ANY($2) AND target_id = ANY($3)
		    AND time > now() - $4::interval
		  GROUP BY bucket ORDER BY bucket`, latencyExpr),
		bucket, srcAgents, dstTargets, window)
	if err != nil {
		return nil, fmt.Errorf("pair series: %w", err)
	}
	defer rows.Close()
	var out []SeriesBucket
	for rows.Next() {
		var b SeriesBucket
		if err := rows.Scan(&b.Bucket, &b.MinUS, &b.AvgUS, &b.MaxUS, &b.LossPct, &b.Samples, &b.Failures); err != nil {
			return nil, fmt.Errorf("pair series: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// PairSummary aggregates one direction (srcAgents → dstTargets) over the
// window, plus the latency source of the newest row so charts and summary
// agree on what "latency" means right now.
func (s *Store) PairSummary(ctx context.Context, srcAgents, dstTargets []uuid.UUID, window time.Duration) (*PairSummaryRow, error) {
	var p PairSummaryRow
	err := s.pool.QueryRow(ctx, fmt.Sprintf(
		`SELECT min(%[1]s)::float8, avg(%[1]s)::float8, max(%[1]s)::float8,
		        100.0 * (1 - sum(received)::float8 / NULLIF(sum(sent), 0)),
		        count(*),
		        max(time) FILTER (WHERE status = 1)
		   FROM probe_results
		  WHERE agent_id = ANY($1) AND target_id = ANY($2)
		    AND time > now() - $3::interval`, latencyExpr),
		srcAgents, dstTargets, window).
		Scan(&p.MinUS, &p.AvgUS, &p.MaxUS, &p.LossPct, &p.Samples, &p.LastOKAt)
	if err != nil {
		return nil, fmt.Errorf("pair summary: %w", err)
	}
	err = s.pool.QueryRow(ctx, fmt.Sprintf(
		`SELECT %s FROM probe_results
		  WHERE agent_id = ANY($1) AND target_id = ANY($2)
		  ORDER BY time DESC LIMIT 1`, latencySourceExpr),
		srcAgents, dstTargets).Scan(&p.LatencySource)
	if err != nil && err != pgx.ErrNoRows {
		return nil, fmt.Errorf("pair summary source: %w", err)
	}
	return &p, nil
}

// DirectionLatest returns the latest row per (agent, target, probe type)
// series for one direction within the horizon — the same shape the matrix
// uses, so cell and pair-detail status agree.
func (s *Store) DirectionLatest(ctx context.Context, srcAgents, dstTargets []uuid.UUID, horizon time.Duration) ([]MatrixRow, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(
		`SELECT DISTINCT ON (agent_id, target_id, probe_type)
		        probe_type, status, time, %s AS latency_us, %s AS latency_source, loss_pct
		   FROM probe_results
		  WHERE agent_id = ANY($1) AND target_id = ANY($2)
		    AND time > now() - $3::interval
		  ORDER BY agent_id, target_id, probe_type, time DESC`,
		latencyExpr, latencySourceExpr),
		srcAgents, dstTargets, horizon)
	if err != nil {
		return nil, fmt.Errorf("direction latest: %w", err)
	}
	defer rows.Close()
	var out []MatrixRow
	for rows.Next() {
		var mr MatrixRow
		if err := rows.Scan(&mr.ProbeType, &mr.Status, &mr.Time, &mr.LatencyUS, &mr.LatencySource, &mr.LossPct); err != nil {
			return nil, fmt.Errorf("direction latest: %w", err)
		}
		out = append(out, mr)
	}
	return out, rows.Err()
}
