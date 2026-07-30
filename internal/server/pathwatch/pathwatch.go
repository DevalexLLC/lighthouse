// Package pathwatch detects traceroute path changes: it keeps the latest
// complete path per series in traceroute_current and records a path_events
// row whenever the agent-computed path_hash changes. Hops live in these
// tables only, never in the probe_results hypertable.
package pathwatch

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DB is the pgx surface Apply needs; satisfied by pgx.Tx and
// *pgxpool.Pool. Apply must run on the ingest transaction.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Run is one genuinely inserted traceroute result. Like outage.Result,
// dedupe-skipped rows must never reach Apply — a replayed traceroute could
// otherwise re-emit a path event.
type Run struct {
	ProbeID     uuid.UUID
	TargetID    uuid.UUID
	Time        time.Time
	DestReached bool
	PathHash    []byte
	Hops        []byte // pre-marshaled JSON array of hops
}

// Change reports one recorded path event, for logging.
type Change struct {
	EventID uuid.UUID
	ProbeID uuid.UUID
}

type current struct {
	updatedAt time.Time
	pathHash  []byte
	hops      []byte
}

type action int

const (
	actionSkip    action = iota // out-of-order or unusable run
	actionInsert                // first sighting: record current, no event
	actionRefresh               // same path: bump updated_at
	actionChange                // different hash: event + update current
)

// decide is the pure per-run decision. Only complete (dest reached) runs
// with a valid 32-byte hash participate — incomplete paths would flap the
// hash on every partial run.
func decide(cur *current, r Run) action {
	if !r.DestReached || len(r.PathHash) != 32 {
		return actionSkip
	}
	if cur == nil {
		return actionInsert
	}
	if !r.Time.After(cur.updatedAt) {
		return actionSkip // out-of-order spool replay
	}
	if string(cur.pathHash) == string(r.PathHash) {
		return actionRefresh
	}
	return actionChange
}

// Apply folds traceroute runs into traceroute_current and path_events
// inside the caller's transaction. Runs may span series; each series is
// processed in time order. The server trusts the agent-computed path_hash
// (the proto documents the derivation); a malformed run is logged and
// skipped, never guessed at.
func Apply(ctx context.Context, db DB, agentID uuid.UUID, runs []Run) ([]Change, error) {
	if len(runs) == 0 {
		return nil, nil
	}

	bySeries := make(map[uuid.UUID][]Run)
	for _, r := range runs {
		if r.DestReached && len(r.PathHash) != 32 {
			slog.Warn("traceroute result with malformed path_hash skipped",
				"agent", agentID, "probe", r.ProbeID, "hash_len", len(r.PathHash))
			continue
		}
		bySeries[r.ProbeID] = append(bySeries[r.ProbeID], r)
	}

	probeIDs := make([]uuid.UUID, 0, len(bySeries))
	for id := range bySeries {
		probeIDs = append(probeIDs, id)
	}
	sort.Slice(probeIDs, func(i, j int) bool {
		return string(probeIDs[i][:]) < string(probeIDs[j][:])
	})

	var changes []Change
	for _, probeID := range probeIDs {
		batch := bySeries[probeID]
		sort.Slice(batch, func(i, j int) bool { return batch[i].Time.Before(batch[j].Time) })

		cur, err := lockCurrent(ctx, db, agentID, probeID)
		if err != nil {
			return nil, err
		}
		for _, r := range batch {
			switch decide(cur, r) {
			case actionSkip:
				continue
			case actionInsert, actionRefresh:
				if err := upsertCurrent(ctx, db, agentID, r); err != nil {
					return nil, err
				}
			case actionChange:
				var eventID uuid.UUID
				err := db.QueryRow(ctx, `
					INSERT INTO path_events (time, agent_id, probe_id, target_id,
						old_path_hash, new_path_hash, old_hops, new_hops)
					VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
					RETURNING id`,
					r.Time, agentID, r.ProbeID, r.TargetID,
					cur.pathHash, r.PathHash, cur.hops, r.Hops).Scan(&eventID)
				if err != nil {
					return nil, fmt.Errorf("insert path event: %w", err)
				}
				if err := upsertCurrent(ctx, db, agentID, r); err != nil {
					return nil, err
				}
				changes = append(changes, Change{EventID: eventID, ProbeID: probeID})
			}
			cur = &current{updatedAt: r.Time, pathHash: r.PathHash, hops: r.Hops}
		}
	}
	return changes, nil
}

func lockCurrent(ctx context.Context, db DB, agentID, probeID uuid.UUID) (*current, error) {
	var cur current
	err := db.QueryRow(ctx, `
		SELECT updated_at, path_hash, hops FROM traceroute_current
		WHERE agent_id = $1 AND probe_id = $2
		FOR UPDATE`, agentID, probeID).Scan(&cur.updatedAt, &cur.pathHash, &cur.hops)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lock traceroute_current: %w", err)
	}
	return &cur, nil
}

func upsertCurrent(ctx context.Context, db DB, agentID uuid.UUID, r Run) error {
	_, err := db.Exec(ctx, `
		INSERT INTO traceroute_current (agent_id, probe_id, target_id, updated_at,
			dest_reached, path_hash, hops)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (agent_id, probe_id) DO UPDATE SET
			target_id = EXCLUDED.target_id,
			updated_at = EXCLUDED.updated_at,
			dest_reached = EXCLUDED.dest_reached,
			path_hash = EXCLUDED.path_hash,
			hops = EXCLUDED.hops`,
		agentID, r.ProbeID, r.TargetID, r.Time, r.DestReached, r.PathHash, r.Hops)
	if err != nil {
		return fmt.Errorf("upsert traceroute_current: %w", err)
	}
	return nil
}
