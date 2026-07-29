package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ResultRow is one probe_results row ready for insertion. Nil pointers map
// to SQL NULL ("not measured" / no error).
type ResultRow struct {
	Time      time.Time
	TargetID  uuid.UUID
	ProbeID   uuid.UUID
	ProbeType int16
	Status    int16
	Sent      int32
	Received  int32
	LossPct   *float32

	RttMinUS    *int32
	RttAvgUS    *int32
	RttMaxUS    *int32
	RttStddevUS *int32
	JitterUS    *int32

	DNSUS          *int32
	TCPConnectUS   *int32
	TLSHandshakeUS *int32
	TTFBUS         *int32
	TotalUS        *int32

	Error *string
}

// InsertResults bulk-inserts a batch for one agent in a single statement.
// The agent ID comes from the caller's authenticated mTLS identity, never
// from the batch. Duplicates (at-least-once spool replay) are silently
// dropped by the dedupe index; the returned count excludes them.
func (s *Store) InsertResults(ctx context.Context, agentID uuid.UUID, rows []ResultRow) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	n := len(rows)
	var (
		times                                  = make([]time.Time, n)
		targetIDs, probeIDs                    = make([]uuid.UUID, n), make([]uuid.UUID, n)
		probeTypes, statuses                   = make([]int16, n), make([]int16, n)
		sents, receiveds                       = make([]int32, n), make([]int32, n)
		lossPcts                               = make([]*float32, n)
		rttMins, rttAvgs, rttMaxs, rttStddevs  = make([]*int32, n), make([]*int32, n), make([]*int32, n), make([]*int32, n)
		jitters, dnss, tcps, tlss, ttfbs, tots = make([]*int32, n), make([]*int32, n), make([]*int32, n), make([]*int32, n), make([]*int32, n), make([]*int32, n)
		errs                                   = make([]*string, n)
	)
	for i, r := range rows {
		times[i] = r.Time
		targetIDs[i], probeIDs[i] = r.TargetID, r.ProbeID
		probeTypes[i], statuses[i] = r.ProbeType, r.Status
		sents[i], receiveds[i] = r.Sent, r.Received
		lossPcts[i] = r.LossPct
		rttMins[i], rttAvgs[i], rttMaxs[i], rttStddevs[i] = r.RttMinUS, r.RttAvgUS, r.RttMaxUS, r.RttStddevUS
		jitters[i], dnss[i], tcps[i], tlss[i], ttfbs[i], tots[i] = r.JitterUS, r.DNSUS, r.TCPConnectUS, r.TLSHandshakeUS, r.TTFBUS, r.TotalUS
		errs[i] = r.Error
	}

	tag, err := s.pool.Exec(ctx, `
		INSERT INTO probe_results (time, agent_id, target_id, probe_id, probe_type, status,
			sent, received, loss_pct, rtt_min_us, rtt_avg_us, rtt_max_us, rtt_stddev_us,
			jitter_us, dns_us, tcp_connect_us, tls_handshake_us, ttfb_us, total_us, error)
		SELECT u.time, $1, u.target_id, u.probe_id, u.probe_type, u.status,
			u.sent, u.received, u.loss_pct, u.rtt_min_us, u.rtt_avg_us, u.rtt_max_us, u.rtt_stddev_us,
			u.jitter_us, u.dns_us, u.tcp_connect_us, u.tls_handshake_us, u.ttfb_us, u.total_us, u.error
		FROM unnest($2::timestamptz[], $3::uuid[], $4::uuid[], $5::smallint[], $6::smallint[],
			$7::int[], $8::int[], $9::real[], $10::int[], $11::int[], $12::int[], $13::int[],
			$14::int[], $15::int[], $16::int[], $17::int[], $18::int[], $19::int[], $20::text[])
			AS u(time, target_id, probe_id, probe_type, status, sent, received, loss_pct,
				rtt_min_us, rtt_avg_us, rtt_max_us, rtt_stddev_us, jitter_us,
				dns_us, tcp_connect_us, tls_handshake_us, ttfb_us, total_us, error)
		ON CONFLICT DO NOTHING`,
		agentID, times, targetIDs, probeIDs, probeTypes, statuses, sents, receiveds, lossPcts,
		rttMins, rttAvgs, rttMaxs, rttStddevs, jitters, dnss, tcps, tlss, ttfbs, tots, errs)
	if err != nil {
		return 0, fmt.Errorf("insert results: %w", err)
	}
	return tag.RowsAffected(), nil
}

// TargetAssignedToAgent reports whether the target is currently assigned to
// the agent — either via a direct probe config for the agent's site, or as
// a mesh peer whose site shares an enabled mesh probe with the agent's site.
// Results for unassigned targets are rejected: direction identity must stay
// unforgeable by construction.
func (s *Store) TargetAssignedToAgent(ctx context.Context, agentID, targetID uuid.UUID) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM probe_configs pc
			JOIN agents me ON me.id = $1
			WHERE pc.site_id = me.site_id AND pc.target_id = $2 AND pc.enabled
		) OR EXISTS(
			SELECT 1 FROM targets t
			JOIN agents peer ON peer.id = t.agent_id
			JOIN agents me ON me.id = $1
			JOIN mesh_members mine ON mine.site_id = me.site_id
			JOIN mesh_members theirs ON theirs.mesh_id = mine.mesh_id AND theirs.site_id = peer.site_id
			JOIN probe_configs pc ON pc.mesh_id = mine.mesh_id AND pc.enabled
			WHERE t.id = $2 AND t.kind = 'agent' AND peer.site_id <> me.site_id
		)`, agentID, targetID).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("target assignment check: %w", err)
	}
	return ok, nil
}
