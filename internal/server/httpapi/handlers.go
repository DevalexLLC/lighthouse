package httpapi

import (
	"net/http"
	"time"

	"github.com/devalexllc/lighthouse/internal/server/store"
)

type siteJSON struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Location    string `json:"location"`
}

func toSiteJSON(sites []store.SiteInfo) []siteJSON {
	out := make([]siteJSON, len(sites))
	for i, s := range sites {
		out[i] = siteJSON{Name: s.Name, DisplayName: s.DisplayName, Location: s.Location}
	}
	return out
}

func (a *api) handleSites(w http.ResponseWriter, r *http.Request) {
	sites, err := a.db.ListSites(r.Context())
	if err != nil {
		internalError(w, "list sites", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sites": toSiteJSON(sites)})
}

func (a *api) handleAgents(w http.ResponseWriter, r *http.Request) {
	agents, err := a.db.ListAgents(r.Context())
	if err != nil {
		internalError(w, "list agents", err)
		return
	}
	type agentJSON struct {
		ID             string     `json:"id"`
		Site           string     `json:"site"`
		Hostname       string     `json:"hostname"`
		ProbeAddress   string     `json:"probe_address"`
		Version        string     `json:"version"`
		LastSeenAt     *time.Time `json:"last_seen_at"`
		EnrolledAt     time.Time  `json:"enrolled_at"`
		ConfigHash     string     `json:"config_hash"`
		CertNotAfter   *time.Time `json:"cert_not_after"`
		CertRevokedAt  *time.Time `json:"cert_revoked_at"`
		Offline        bool       `json:"offline"`
		ProbesFailing  int64      `json:"probes_failing"`
		ProbesTotal    int64      `json:"probes_total"`
		DroppedResults int64      `json:"dropped_results"`
		LastDroppedAt  *time.Time `json:"last_dropped_at"`
	}
	out := make([]agentJSON, len(agents))
	for i, ag := range agents {
		out[i] = agentJSON{
			ID: ag.ID.String(), Site: ag.Site, Hostname: ag.Hostname,
			ProbeAddress: ag.ProbeAddress, Version: ag.Version, LastSeenAt: ag.LastSeenAt,
			EnrolledAt: ag.CreatedAt, ConfigHash: ag.ConfigHash,
			CertNotAfter: ag.CertNotAfter, CertRevokedAt: ag.CertRevokedAt,
			Offline: ag.Offline, ProbesFailing: ag.ProbesFailing, ProbesTotal: ag.ProbesTotal,
			DroppedResults: ag.DroppedResults, LastDroppedAt: ag.LastDroppedAt,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": out})
}

func (a *api) handleMatrix(w http.ResponseWriter, r *http.Request) {
	sites, err := a.db.ListSites(r.Context())
	if err != nil {
		internalError(w, "list sites", err)
		return
	}
	rows, err := a.db.MatrixLatest(r.Context(), staleHorizon)
	if err != nil {
		internalError(w, "matrix", err)
		return
	}
	expected, err := a.db.ExpectedPairs(r.Context())
	if err != nil {
		internalError(w, "expected pairs", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sites":     toSiteJSON(sites),
		"cells":     foldMatrix(rows, expected),
		"horizon_s": int(staleHorizon.Seconds()),
	})
}

// pairEndpoints resolves both site path segments or answers the request
// itself (404 with the offending name) and returns ok=false.
func (a *api) pairEndpoints(w http.ResponseWriter, r *http.Request) (ea, eb *store.SiteEndpoints, ok bool) {
	for _, seg := range []struct {
		name string
		dst  **store.SiteEndpoints
	}{{r.PathValue("a"), &ea}, {r.PathValue("b"), &eb}} {
		ep, err := a.db.SiteEndpoints(r.Context(), seg.name)
		if err != nil {
			internalError(w, "site endpoints", err)
			return nil, nil, false
		}
		if ep == nil {
			writeError(w, http.StatusNotFound, "unknown site "+seg.name)
			return nil, nil, false
		}
		*seg.dst = ep
	}
	return ea, eb, true
}

// Percentile fields carry omitempty: they exist only for aggregate-sourced
// windows (30d+), and their absence is how a client knows a raw window has
// no percentile data rather than a measured zero.
type latencyJSON struct {
	MinUS *float64 `json:"min_us"`
	AvgUS *float64 `json:"avg_us"`
	MaxUS *float64 `json:"max_us"`
	P50US *float64 `json:"p50_us,omitempty"`
	P95US *float64 `json:"p95_us,omitempty"`
	P99US *float64 `json:"p99_us,omitempty"`
}

type directionJSON struct {
	Status            string      `json:"status"`
	LastOKAt          *time.Time  `json:"last_ok_at"`
	Latency           latencyJSON `json:"latency"`
	LatencySource     string      `json:"latency_source"`
	LossPct           *float64    `json:"loss_pct"`
	Samples           int64       `json:"samples"`
	JitterAvgUS       *float64    `json:"jitter_avg_us"`
	TCPConnectAvgUS   *float64    `json:"tcp_connect_avg_us"`
	TLSHandshakeAvgUS *float64    `json:"tls_handshake_avg_us"`
	Checks            []probeJSON `json:"checks"`
}

// direction assembles one direction's summary (aggregates over the window,
// status from the latest results inside the staleness horizon).
func (a *api) direction(r *http.Request, src, dst *store.SiteEndpoints, spec windowSpec) (directionJSON, error) {
	sum, err := a.db.PairSummary(r.Context(), src.AgentIDs, dst.TargetIDs, spec.Window, spec.Source)
	if err != nil {
		return directionJSON{}, err
	}
	latest, err := a.db.DirectionLatest(r.Context(), src.AgentIDs, dst.TargetIDs, staleHorizon)
	if err != nil {
		return directionJSON{}, err
	}
	checks := make([]probeJSON, len(latest))
	for i, row := range latest {
		checks[i] = toProbeJSON(row)
	}
	return directionJSON{
		Status:   directionStatus(latest),
		LastOKAt: sum.LastOKAt,
		Latency: latencyJSON{
			MinUS: sum.MinUS, AvgUS: sum.AvgUS, MaxUS: sum.MaxUS,
			P50US: sum.P50US, P95US: sum.P95US, P99US: sum.P99US,
		},
		LatencySource:     sum.LatencySource,
		LossPct:           sum.LossPct,
		Samples:           sum.Samples,
		JitterAvgUS:       sum.JitterAvgUS,
		TCPConnectAvgUS:   sum.TCPConnectAvgUS,
		TLSHandshakeAvgUS: sum.TLSHandshakeAvgUS,
		Checks:            checks,
	}, nil
}

func (a *api) handlePair(w http.ResponseWriter, r *http.Request) {
	spec, ok := parseWindow(r.URL.Query().Get("window"))
	if !ok {
		writeError(w, http.StatusBadRequest, "unknown window (want 24h|7d|30d|90d|365d)")
		return
	}
	ea, eb, ok := a.pairEndpoints(w, r)
	if !ok {
		return
	}
	aToB, err := a.direction(r, ea, eb, spec)
	if err != nil {
		internalError(w, "pair a→b", err)
		return
	}
	bToA, err := a.direction(r, eb, ea, spec)
	if err != nil {
		internalError(w, "pair b→a", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"a": ea.Name, "b": eb.Name,
		"window": windowName(r), "source": string(spec.Source),
		"a_to_b": aToB, "b_to_a": bToA,
	})
}

type pointJSON struct {
	T        int64    `json:"t"`
	MinUS    *float64 `json:"min_us"`
	AvgUS    *float64 `json:"avg_us"`
	MaxUS    *float64 `json:"max_us"`
	LossPct  *float64 `json:"loss_pct"`
	Samples  int64    `json:"samples"`
	Failures int64    `json:"failures"`
	P50US    *float64 `json:"p50_us,omitempty"`
	P95US    *float64 `json:"p95_us,omitempty"`
	P99US    *float64 `json:"p99_us,omitempty"`
}

func toPoints(buckets []store.SeriesBucket) []pointJSON {
	out := make([]pointJSON, len(buckets))
	for i, b := range buckets {
		out[i] = pointJSON{
			T: b.Bucket.Unix(), MinUS: b.MinUS, AvgUS: b.AvgUS, MaxUS: b.MaxUS,
			LossPct: b.LossPct, Samples: b.Samples, Failures: b.Failures,
			P50US: b.P50US, P95US: b.P95US, P99US: b.P99US,
		}
	}
	return out
}

func (a *api) handleSeries(w http.ResponseWriter, r *http.Request) {
	metric := r.URL.Query().Get("metric")
	if metric == "" {
		metric = "latency"
	}
	if metric != "latency" && metric != "loss" {
		writeError(w, http.StatusBadRequest, "unknown metric (want latency|loss)")
		return
	}
	spec, ok := parseWindow(r.URL.Query().Get("window"))
	if !ok {
		writeError(w, http.StatusBadRequest, "unknown window (want 24h|7d|30d|90d|365d)")
		return
	}
	ea, eb, ok := a.pairEndpoints(w, r)
	if !ok {
		return
	}
	aSource, err := a.db.PairLatencySource(r.Context(), ea.AgentIDs, eb.TargetIDs, spec.Window, spec.Source)
	if err != nil {
		internalError(w, "series a→b source", err)
		return
	}
	bSource, err := a.db.PairLatencySource(r.Context(), eb.AgentIDs, ea.TargetIDs, spec.Window, spec.Source)
	if err != nil {
		internalError(w, "series b→a source", err)
		return
	}
	aToB, err := a.db.PairSeries(r.Context(), ea.AgentIDs, eb.TargetIDs, spec.Bucket, spec.Window, spec.Source, aSource)
	if err != nil {
		internalError(w, "series a→b", err)
		return
	}
	bToA, err := a.db.PairSeries(r.Context(), eb.AgentIDs, ea.TargetIDs, spec.Bucket, spec.Window, spec.Source, bSource)
	if err != nil {
		internalError(w, "series b→a", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"metric": metric, "window": windowName(r),
		"resolution_s": int(spec.Bucket.Seconds()),
		"source":       string(spec.Source),
		// Top-level latency_source predates directional sources; it stays
		// as the a_to_b alias so pre-M5 clients keep an honest axis label.
		"latency_source": aSource,
		"a_to_b":         map[string]any{"latency_source": aSource, "points": toPoints(aToB)},
		"b_to_a":         map[string]any{"latency_source": bSource, "points": toPoints(bToA)},
	})
}

// windowName echoes the validated ?window= (default 24h) back to the client.
func windowName(r *http.Request) string {
	if w := r.URL.Query().Get("window"); w != "" {
		return w
	}
	return "24h"
}
