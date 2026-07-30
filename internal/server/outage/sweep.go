package outage

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// offlineSeenGrace is how stale agents.last_seen_at must be before an agent
// counts as offline: 4× the 30 s config-stream touch, so one missed tick
// never trips it.
const offlineSeenGrace = 2 * time.Minute

// SweepConfig configures the silence sweep. Zero Interval means 30 s.
type SweepConfig struct {
	Interval time.Duration
}

// Sweep periodically opens and closes agent_offline events until ctx is
// done. It is result- and stream-silence driven: an agent with no result in
// 3× its fastest probe interval AND a stale last_seen_at gets exactly one
// open event (the partial unique index resolves races); the event closes on
// the first sweep after either signal resumes. Agents that never connected
// are onboarding problems, not outages, and are skipped.
func Sweep(ctx context.Context, db DB, cfg SweepConfig) {
	interval := cfg.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := sweepOnce(ctx, db, time.Now()); err != nil && ctx.Err() == nil {
				slog.Error("agent_offline sweep failed", "err", err)
			}
		}
	}
}

// agentSignals is everything the offline decision needs for one agent.
type agentSignals struct {
	AgentID     uuid.UUID
	Hostname    string
	LastSeen    time.Time
	MinInterval time.Duration // fastest applicable probe interval; 0 = none configured
	LastResult  time.Time     // newest series_state.last_time; zero = none
	OpenEventID *uuid.UUID    // open agent_offline event, if any
}

// decideOffline is the pure decision: open when both signals are silent,
// close when either resumes.
func decideOffline(now time.Time, s agentSignals) (open, closeEvent bool) {
	// With no configured probes there is no result cadence to miss; the
	// stream heartbeat alone decides.
	resultSilent := s.LastResult.IsZero() || s.MinInterval <= 0 ||
		now.Sub(s.LastResult) > 3*s.MinInterval
	seenSilent := now.Sub(s.LastSeen) > offlineSeenGrace
	silent := resultSilent && seenSilent
	if s.OpenEventID == nil {
		return silent, false
	}
	return false, !silent
}

func sweepOnce(ctx context.Context, db DB, now time.Time) error {
	rows, err := db.Query(ctx, `
		SELECT a.id, a.hostname, a.last_seen_at,
			(SELECT min(pc.interval_ms) FROM probe_configs pc
			 WHERE pc.enabled AND (
				pc.site_id = a.site_id
				OR (pc.mesh_id IS NOT NULL
					AND EXISTS (SELECT 1 FROM mesh_members mm
						WHERE mm.mesh_id = pc.mesh_id AND mm.site_id = a.site_id)
					AND EXISTS (SELECT 1 FROM mesh_members mo
						WHERE mo.mesh_id = pc.mesh_id AND mo.site_id <> a.site_id)))),
			(SELECT max(ss.last_time) FROM series_state ss WHERE ss.agent_id = a.id),
			(SELECT oe.id FROM outage_events oe
			 WHERE oe.agent_id = a.id AND oe.kind = 'agent_offline' AND oe.closed_at IS NULL)
		FROM agents a
		WHERE a.last_seen_at IS NOT NULL`)
	if err != nil {
		return fmt.Errorf("sweep query: %w", err)
	}
	defer rows.Close()

	var agents []agentSignals
	for rows.Next() {
		var (
			s          agentSignals
			intervalMS *int32
			lastResult *time.Time
		)
		if err := rows.Scan(&s.AgentID, &s.Hostname, &s.LastSeen, &intervalMS, &lastResult, &s.OpenEventID); err != nil {
			return fmt.Errorf("sweep scan: %w", err)
		}
		if intervalMS != nil {
			s.MinInterval = time.Duration(*intervalMS) * time.Millisecond
		}
		if lastResult != nil {
			s.LastResult = *lastResult
		}
		agents = append(agents, s)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("sweep query: %w", err)
	}

	for _, s := range agents {
		open, closeEvent := decideOffline(now, s)
		switch {
		case open:
			// opened_at is the last evidence of life, not sweep time.
			openedAt := s.LastSeen
			if s.LastResult.After(openedAt) {
				openedAt = s.LastResult
			}
			tag, err := db.Exec(ctx, `
				INSERT INTO outage_events (kind, agent_id, opened_at)
				VALUES ('agent_offline', $1, $2)
				ON CONFLICT (agent_id) WHERE kind = 'agent_offline' AND closed_at IS NULL
				DO NOTHING`, s.AgentID, openedAt)
			if err != nil {
				return fmt.Errorf("open agent_offline: %w", err)
			}
			if tag.RowsAffected() > 0 {
				slog.Warn("agent offline", "agent", s.AgentID, "hostname", s.Hostname, "since", openedAt)
			}
		case closeEvent:
			if _, err := db.Exec(ctx, `
				UPDATE outage_events SET closed_at = $2 WHERE id = $1 AND closed_at IS NULL`,
				*s.OpenEventID, now); err != nil {
				return fmt.Errorf("close agent_offline: %w", err)
			}
			slog.Info("agent back online", "agent", s.AgentID, "hostname", s.Hostname)
		}
	}
	return nil
}
