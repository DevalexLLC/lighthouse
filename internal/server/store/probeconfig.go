package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/devalexllc/lighthouse/internal/server/probeid"
)

// ErrNotFound marks admin lookups that resolved nothing; httpapi maps it to
// 404 without string matching. Match with errors.Is.
var ErrNotFound = errors.New("not found")

// notFoundError keeps each site's exact human-readable message (the CLI
// prints these verbatim) while still matching errors.Is(err, ErrNotFound).
type notFoundError struct{ msg string }

func (e notFoundError) Error() string        { return e.msg }
func (e notFoundError) Is(target error) bool { return target == ErrNotFound }

func notFoundf(format string, args ...any) error {
	return notFoundError{msg: fmt.Sprintf(format, args...)}
}

// ErrConflict marks admin writes refused because they collide with an
// existing row of a different kind; httpapi maps it to 409.
var ErrConflict = errors.New("conflict")

type conflictError struct{ msg string }

func (e conflictError) Error() string        { return e.msg }
func (e conflictError) Is(target error) bool { return target == ErrConflict }

func conflictf(format string, args ...any) error {
	return conflictError{msg: fmt.Sprintf(format, args...)}
}

// InUseError reports a target delete blocked by referencing probe configs;
// httpapi maps it to 409.
type InUseError struct {
	Name  string
	Count int64
}

func (e InUseError) Error() string {
	return fmt.Sprintf("target %q is referenced by %d probe config(s)", e.Name, e.Count)
}

// TargetInfo is a targets row as shown by the admin CLI.
type TargetInfo struct {
	ID         uuid.UUID
	Kind       string
	Name       string
	AgentID    *uuid.UUID
	Address    string
	Port       int32
	URL        string
	ProbeCount int64
	CreatedAt  time.Time
}

// MeshGroupInfo is a mesh group with its member site names.
type MeshGroupInfo struct {
	ID         uuid.UUID
	Name       string
	Sites      []string
	ProbeCount int64
}

// ProbeConfigInfo is a probe_configs row as shown by the admin CLI. Exactly
// one of Site/Target (direct) or Mesh (template) is set.
type ProbeConfigInfo struct {
	ID           uuid.UUID
	Site         string
	Target       string
	Mesh         string
	ProbeType    int16
	Interval     time.Duration
	Timeout      time.Duration
	TrainCount   int32
	TrainSpacing time.Duration
	Params       map[string]string
	Enabled      bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
	UpdatedBy    string
}

// ProbeSettings are the type/cadence knobs shared by direct and mesh probes.
type ProbeSettings struct {
	ProbeType    int16
	Interval     time.Duration
	Timeout      time.Duration
	TrainCount   int32
	TrainSpacing time.Duration
	Params       map[string]string
}

// UpsertExternalTarget creates or updates an external target by name and
// returns its ID. Idempotent so dev bootstrap can re-run safely.
func (s *Store) UpsertExternalTarget(ctx context.Context, name, address string, port int32, url string) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.pool.QueryRow(ctx, `
		INSERT INTO targets (kind, name, address, port, url)
		VALUES ('external', $1, $2, $3, $4)
		ON CONFLICT (name) DO UPDATE
			SET address = EXCLUDED.address, port = EXCLUDED.port, url = EXCLUDED.url
			WHERE targets.kind = 'external'
		RETURNING id`, name, address, port, url).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, conflictf("target %q already exists as an agent target", name)
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("upsert target %q: %w", name, err)
	}
	return id, nil
}

// ListTargets returns all targets, agents included, each with the number of
// probe configs referencing it (the UI blocks deletes while in use).
func (s *Store) ListTargets(ctx context.Context) ([]TargetInfo, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, kind, name, agent_id, address, port, url, created_at,
		       (SELECT count(*) FROM probe_configs pc WHERE pc.target_id = targets.id)
		FROM targets ORDER BY kind, name`)
	if err != nil {
		return nil, fmt.Errorf("list targets: %w", err)
	}
	defer rows.Close()
	var out []TargetInfo
	for rows.Next() {
		var t TargetInfo
		if err := rows.Scan(&t.ID, &t.Kind, &t.Name, &t.AgentID, &t.Address, &t.Port, &t.URL, &t.CreatedAt, &t.ProbeCount); err != nil {
			return nil, fmt.Errorf("list targets: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteTarget removes an external target by name. Agent targets cannot be
// deleted (they go away with the agent); a target referenced by probe
// configs returns InUseError naming the count. The FOR UPDATE lock blocks a
// concurrent probe add from committing a new reference mid-delete (FK
// checks take a key-share lock, which conflicts).
func (s *Store) DeleteTarget(ctx context.Context, name string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("delete target %q: %w", name, err)
	}
	defer tx.Rollback(ctx)

	var id uuid.UUID
	err = tx.QueryRow(ctx, `
		SELECT id FROM targets WHERE name = $1 AND kind = 'external' FOR UPDATE`, name).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return notFoundf("external target %q does not exist", name)
	}
	if err != nil {
		return fmt.Errorf("delete target %q: %w", name, err)
	}
	var inUse int64
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM probe_configs WHERE target_id = $1`, id).Scan(&inUse); err != nil {
		return fmt.Errorf("delete target %q: %w", name, err)
	}
	if inUse > 0 {
		return InUseError{Name: name, Count: inUse}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM targets WHERE id = $1`, id); err != nil {
		return fmt.Errorf("delete target %q: %w", name, err)
	}
	return tx.Commit(ctx)
}

// UpsertMeshGroup creates a mesh group if it does not exist and returns its ID.
func (s *Store) UpsertMeshGroup(ctx context.Context, name string) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.pool.QueryRow(ctx, `
		INSERT INTO mesh_groups (name) VALUES ($1)
		ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name
		RETURNING id`, name).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("upsert mesh group %q: %w", name, err)
	}
	return id, nil
}

// SiteIDByName resolves a site name WITHOUT creating it — admin commands
// against a typo'd site must fail loudly, unlike token creation.
func (s *Store) SiteIDByName(ctx context.Context, name string) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.pool.QueryRow(ctx, `SELECT id FROM sites WHERE name = $1`, name).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, notFoundf("site %q does not exist", name)
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("resolve site %q: %w", name, err)
	}
	return id, nil
}

func (s *Store) meshIDByName(ctx context.Context, name string) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.pool.QueryRow(ctx, `SELECT id FROM mesh_groups WHERE name = $1`, name).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, notFoundf("mesh group %q does not exist", name)
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("resolve mesh group %q: %w", name, err)
	}
	return id, nil
}

func (s *Store) targetIDByName(ctx context.Context, name string) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.pool.QueryRow(ctx, `SELECT id FROM targets WHERE name = $1`, name).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, notFoundf("target %q does not exist", name)
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("resolve target %q: %w", name, err)
	}
	return id, nil
}

// AddMeshMember adds a site to a mesh group. Idempotent.
func (s *Store) AddMeshMember(ctx context.Context, meshName, siteName string) error {
	meshID, err := s.meshIDByName(ctx, meshName)
	if err != nil {
		return err
	}
	siteID, err := s.SiteIDByName(ctx, siteName)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO mesh_members (mesh_id, site_id) VALUES ($1, $2)
		ON CONFLICT DO NOTHING`, meshID, siteID)
	if err != nil {
		return fmt.Errorf("add %q to mesh %q: %w", siteName, meshName, err)
	}
	return nil
}

// RemoveMeshMember removes a site from a mesh group and cleans up the
// series of every expanded probe involving that site (both directions, per
// template) — those series stop producing results, so their open incidents
// would otherwise stay active forever.
func (s *Store) RemoveMeshMember(ctx context.Context, meshName, siteName string) error {
	meshID, err := s.meshIDByName(ctx, meshName)
	if err != nil {
		return err
	}
	siteID, err := s.SiteIDByName(ctx, siteName)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("remove %q from mesh %q: %w", siteName, meshName, err)
	}
	defer tx.Rollback(ctx)

	templates, err := meshTemplateTypes(ctx, tx, meshID)
	if err != nil {
		return fmt.Errorf("remove %q from mesh %q: %w", siteName, meshName, err)
	}
	members, err := meshMemberSiteIDs(ctx, tx, meshID)
	if err != nil {
		return fmt.Errorf("remove %q from mesh %q: %w", siteName, meshName, err)
	}
	var ids []uuid.UUID
	for _, typ := range templates {
		for _, other := range members {
			if other == siteID {
				continue
			}
			ids = append(ids,
				probeid.MeshProbeID(meshID, siteID, other, typ),
				probeid.MeshProbeID(meshID, other, siteID, typ))
		}
	}
	if err := cleanupSeries(ctx, tx, ids, true); err != nil {
		return fmt.Errorf("remove %q from mesh %q: %w", siteName, meshName, err)
	}

	tag, err := tx.Exec(ctx, `
		DELETE FROM mesh_members WHERE mesh_id = $1 AND site_id = $2`, meshID, siteID)
	if err != nil {
		return fmt.Errorf("remove %q from mesh %q: %w", siteName, meshName, err)
	}
	if tag.RowsAffected() == 0 {
		return notFoundf("site %q is not a member of mesh %q", siteName, meshName)
	}
	return tx.Commit(ctx)
}

// ListMeshGroups returns all mesh groups with their member site names and
// the number of probe templates on each (the UI surfaces delete blast
// radius).
func (s *Store) ListMeshGroups(ctx context.Context) ([]MeshGroupInfo, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT g.id, g.name, COALESCE(array_agg(s.name ORDER BY s.name) FILTER (WHERE s.name IS NOT NULL), '{}'),
		       (SELECT count(*) FROM probe_configs pc WHERE pc.mesh_id = g.id)
		FROM mesh_groups g
		LEFT JOIN mesh_members m ON m.mesh_id = g.id
		LEFT JOIN sites s ON s.id = m.site_id
		GROUP BY g.id, g.name ORDER BY g.name`)
	if err != nil {
		return nil, fmt.Errorf("list mesh groups: %w", err)
	}
	defer rows.Close()
	var out []MeshGroupInfo
	for rows.Next() {
		var g MeshGroupInfo
		if err := rows.Scan(&g.ID, &g.Name, &g.Sites, &g.ProbeCount); err != nil {
			return nil, fmt.Errorf("list mesh groups: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// DeleteMeshGroup removes a mesh group; the FK cascades its memberships and
// probe templates. Every expanded series is cleaned up first so no open
// incident outlives its probe. Returns how many probe templates went with
// the mesh so callers can surface the blast radius.
func (s *Store) DeleteMeshGroup(ctx context.Context, name string) (int64, error) {
	meshID, err := s.meshIDByName(ctx, name)
	if err != nil {
		return 0, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("delete mesh %q: %w", name, err)
	}
	defer tx.Rollback(ctx)

	templates, err := meshTemplateTypes(ctx, tx, meshID)
	if err != nil {
		return 0, fmt.Errorf("delete mesh %q: %w", name, err)
	}
	members, err := meshMemberSiteIDs(ctx, tx, meshID)
	if err != nil {
		return 0, fmt.Errorf("delete mesh %q: %w", name, err)
	}
	if err := cleanupSeries(ctx, tx, expandMeshProbeIDs(meshID, templates, members), true); err != nil {
		return 0, fmt.Errorf("delete mesh %q: %w", name, err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM mesh_groups WHERE id = $1`, meshID); err != nil {
		return 0, fmt.Errorf("delete mesh %q: %w", name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("delete mesh %q: %w", name, err)
	}
	return int64(len(templates)), nil
}

// AddDirectProbe assigns a probe of target to every agent at site.
func (s *Store) AddDirectProbe(ctx context.Context, siteName, targetName string, ps ProbeSettings, updatedBy string) (uuid.UUID, error) {
	siteID, err := s.SiteIDByName(ctx, siteName)
	if err != nil {
		return uuid.Nil, err
	}
	targetID, err := s.targetIDByName(ctx, targetName)
	if err != nil {
		return uuid.Nil, err
	}
	var id uuid.UUID
	err = s.pool.QueryRow(ctx, `
		INSERT INTO probe_configs (site_id, target_id, probe_type, interval_ms, timeout_ms, train_count, train_spacing_ms, params, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id`,
		siteID, targetID, ps.ProbeType, ps.Interval.Milliseconds(), ps.Timeout.Milliseconds(),
		ps.TrainCount, ps.TrainSpacing.Milliseconds(), ps.Params, updatedBy).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("add probe: %w", err)
	}
	return id, nil
}

// AddMeshProbe creates a mesh probe template expanded over ordered site pairs.
func (s *Store) AddMeshProbe(ctx context.Context, meshName string, ps ProbeSettings, updatedBy string) (uuid.UUID, error) {
	meshID, err := s.meshIDByName(ctx, meshName)
	if err != nil {
		return uuid.Nil, err
	}
	var id uuid.UUID
	err = s.pool.QueryRow(ctx, `
		INSERT INTO probe_configs (mesh_id, probe_type, interval_ms, timeout_ms, train_count, train_spacing_ms, params, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id`,
		meshID, ps.ProbeType, ps.Interval.Milliseconds(), ps.Timeout.Milliseconds(),
		ps.TrainCount, ps.TrainSpacing.Milliseconds(), ps.Params, updatedBy).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("add mesh probe: %w", err)
	}
	return id, nil
}

const probeConfigSelect = `
	SELECT pc.id, COALESCE(s.name, ''), COALESCE(t.name, ''), COALESCE(g.name, ''),
	       pc.probe_type, pc.interval_ms, pc.timeout_ms, pc.train_count, pc.train_spacing_ms,
	       pc.params, pc.enabled, pc.created_at, pc.updated_at, pc.updated_by
	FROM probe_configs pc
	LEFT JOIN sites s ON s.id = pc.site_id
	LEFT JOIN targets t ON t.id = pc.target_id
	LEFT JOIN mesh_groups g ON g.id = pc.mesh_id`

func scanProbeConfig(row pgx.Row) (ProbeConfigInfo, error) {
	var (
		p                                     ProbeConfigInfo
		intervalMS, timeoutMS, trainSpacingMS int64
	)
	err := row.Scan(&p.ID, &p.Site, &p.Target, &p.Mesh, &p.ProbeType,
		&intervalMS, &timeoutMS, &p.TrainCount, &trainSpacingMS, &p.Params, &p.Enabled,
		&p.CreatedAt, &p.UpdatedAt, &p.UpdatedBy)
	if err != nil {
		return p, err
	}
	p.Interval = time.Duration(intervalMS) * time.Millisecond
	p.Timeout = time.Duration(timeoutMS) * time.Millisecond
	p.TrainSpacing = time.Duration(trainSpacingMS) * time.Millisecond
	return p, nil
}

// ListProbeConfigs returns every probe config with names resolved.
func (s *Store) ListProbeConfigs(ctx context.Context) ([]ProbeConfigInfo, error) {
	rows, err := s.pool.Query(ctx, probeConfigSelect+` ORDER BY pc.created_at`)
	if err != nil {
		return nil, fmt.Errorf("list probe configs: %w", err)
	}
	defer rows.Close()
	var out []ProbeConfigInfo
	for rows.Next() {
		p, err := scanProbeConfig(rows)
		if err != nil {
			return nil, fmt.Errorf("list probe configs: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetProbeConfig returns one probe config with names resolved.
func (s *Store) GetProbeConfig(ctx context.Context, id uuid.UUID) (*ProbeConfigInfo, error) {
	p, err := scanProbeConfig(s.pool.QueryRow(ctx, probeConfigSelect+` WHERE pc.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, notFoundf("probe config %s does not exist", id)
	}
	if err != nil {
		return nil, fmt.Errorf("get probe config: %w", err)
	}
	return &p, nil
}

// UpdateProbeConfig edits a probe's cadence/train/params/enabled in place —
// identity (type, assignment) is immutable, enforced by callers, so the
// probe ID and its series history stay continuous. Disabling cleans up the
// expanded series (open incidents close; counters reset) because a disabled
// probe stops producing the results that would ever close them.
func (s *Store) UpdateProbeConfig(ctx context.Context, id uuid.UUID, ps ProbeSettings, enabled bool, updatedBy string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("update probe config: %w", err)
	}
	defer tx.Rollback(ctx)

	var (
		meshID          *uuid.UUID
		probeType       int16
		wasEnabled      bool
	)
	err = tx.QueryRow(ctx, `
		SELECT mesh_id, probe_type, enabled FROM probe_configs WHERE id = $1 FOR UPDATE`,
		id).Scan(&meshID, &probeType, &wasEnabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return notFoundf("probe config %s does not exist", id)
	}
	if err != nil {
		return fmt.Errorf("update probe config: %w", err)
	}

	if wasEnabled && !enabled {
		ids, err := expandedProbeIDs(ctx, tx, id, meshID, probeType)
		if err != nil {
			return fmt.Errorf("update probe config: %w", err)
		}
		if err := cleanupSeries(ctx, tx, ids, false); err != nil {
			return fmt.Errorf("update probe config: %w", err)
		}
	}

	_, err = tx.Exec(ctx, `
		UPDATE probe_configs
		SET interval_ms = $2, timeout_ms = $3, train_count = $4, train_spacing_ms = $5,
		    params = $6, enabled = $7, updated_at = now(), updated_by = $8
		WHERE id = $1`,
		id, ps.Interval.Milliseconds(), ps.Timeout.Milliseconds(), ps.TrainCount,
		ps.TrainSpacing.Milliseconds(), ps.Params, enabled, updatedBy)
	if err != nil {
		return fmt.Errorf("update probe config: %w", err)
	}
	return tx.Commit(ctx)
}

// DeleteProbeConfig removes a probe config by ID, cleaning up the expanded
// series first (open incidents close; series/traceroute state is deleted).
func (s *Store) DeleteProbeConfig(ctx context.Context, id uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("delete probe config: %w", err)
	}
	defer tx.Rollback(ctx)

	var (
		meshID    *uuid.UUID
		probeType int16
	)
	err = tx.QueryRow(ctx, `
		SELECT mesh_id, probe_type FROM probe_configs WHERE id = $1 FOR UPDATE`,
		id).Scan(&meshID, &probeType)
	if errors.Is(err, pgx.ErrNoRows) {
		return notFoundf("probe config %s does not exist", id)
	}
	if err != nil {
		return fmt.Errorf("delete probe config: %w", err)
	}

	ids, err := expandedProbeIDs(ctx, tx, id, meshID, probeType)
	if err != nil {
		return fmt.Errorf("delete probe config: %w", err)
	}
	if err := cleanupSeries(ctx, tx, ids, true); err != nil {
		return fmt.Errorf("delete probe config: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM probe_configs WHERE id = $1`, id); err != nil {
		return fmt.Errorf("delete probe config: %w", err)
	}
	return tx.Commit(ctx)
}

// meshTemplateTypes returns the probe types of every template on a mesh
// (duplicates included — expansion is per template row).
func meshTemplateTypes(ctx context.Context, tx pgx.Tx, meshID uuid.UUID) ([]int16, error) {
	rows, err := tx.Query(ctx, `SELECT probe_type FROM probe_configs WHERE mesh_id = $1`, meshID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int16
	for rows.Next() {
		var t int16
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func meshMemberSiteIDs(ctx context.Context, tx pgx.Tx, meshID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := tx.Query(ctx, `SELECT site_id FROM mesh_members WHERE mesh_id = $1`, meshID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// expandMeshProbeIDs derives the probe ID of every ordered site pair for
// each template type — the same derivation meshexpand uses to build
// snapshots, so cleanup hits exactly the series agents were running.
func expandMeshProbeIDs(meshID uuid.UUID, templateTypes []int16, members []uuid.UUID) []uuid.UUID {
	var ids []uuid.UUID
	for _, typ := range templateTypes {
		for _, src := range members {
			for _, dst := range members {
				if src == dst {
					continue
				}
				ids = append(ids, probeid.MeshProbeID(meshID, src, dst, typ))
			}
		}
	}
	return ids
}

// expandedProbeIDs resolves the concrete probe IDs one config row expands
// to: the row's own ID for a direct probe, or every ordered-pair derivation
// for a mesh template.
func expandedProbeIDs(ctx context.Context, tx pgx.Tx, id uuid.UUID, meshID *uuid.UUID, probeType int16) ([]uuid.UUID, error) {
	if meshID == nil {
		return []uuid.UUID{id}, nil
	}
	members, err := meshMemberSiteIDs(ctx, tx, *meshID)
	if err != nil {
		return nil, err
	}
	return expandMeshProbeIDs(*meshID, []int16{probeType}, members), nil
}

// cleanupSeries closes the open probe_failing event of every listed series
// — the probe stops producing results, so ingest's 3-OK close can never
// happen; closed_at = now() honestly records when monitoring was removed.
// deleteRows additionally drops series_state and traceroute_current (the
// probe is gone for good); a disable instead resets the hysteresis
// counters but keeps last_time, so a spool replay after re-enable still
// dedupes against out-of-order stragglers.
func cleanupSeries(ctx context.Context, tx pgx.Tx, probeIDs []uuid.UUID, deleteRows bool) error {
	if len(probeIDs) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE outage_events SET closed_at = now()
		WHERE probe_id = ANY($1) AND kind = 'probe_failing' AND closed_at IS NULL`, probeIDs); err != nil {
		return fmt.Errorf("close open events: %w", err)
	}
	if deleteRows {
		if _, err := tx.Exec(ctx, `DELETE FROM series_state WHERE probe_id = ANY($1)`, probeIDs); err != nil {
			return fmt.Errorf("delete series state: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM traceroute_current WHERE probe_id = ANY($1)`, probeIDs); err != nil {
			return fmt.Errorf("delete traceroute state: %w", err)
		}
		return nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE series_state
		SET open_event_id = NULL, consec_fails = 0, consec_oks = 0,
		    first_fail_at = NULL, first_ok_at = NULL
		WHERE probe_id = ANY($1)`, probeIDs); err != nil {
		return fmt.Errorf("reset series state: %w", err)
	}
	return nil
}

// DirectProbeRow is a direct (site-scoped) probe assignment for one agent.
type DirectProbeRow struct {
	ID       uuid.UUID
	Settings ProbeSettings
	TargetID uuid.UUID
	Kind     string
	Address  string
	Port     int32
	URL      string
}

// MeshProbeRow is a mesh probe template applying to the agent's site.
type MeshProbeRow struct {
	MeshID   uuid.UUID
	Settings ProbeSettings
}

// PeerRow is a peer agent reachable through a shared mesh.
type PeerRow struct {
	MeshID       uuid.UUID
	AgentID      uuid.UUID
	SiteID       uuid.UUID
	TargetID     uuid.UUID // the peer's agent-kind targets.id
	ProbeAddress string
}

// AgentConfigInputs is everything needed to expand one agent's config
// snapshot (consumed by meshexpand, which is pure).
type AgentConfigInputs struct {
	AgentID uuid.UUID
	SiteID  uuid.UUID
	Direct  []DirectProbeRow
	Mesh    []MeshProbeRow
	Peers   []PeerRow
}

// LoadAgentConfigInputs gathers the agent's site, its site's direct probes,
// mesh templates covering the site, and mesh peers — one batched round trip.
func (s *Store) LoadAgentConfigInputs(ctx context.Context, agentID uuid.UUID) (AgentConfigInputs, error) {
	in := AgentConfigInputs{AgentID: agentID}

	batch := &pgx.Batch{}
	batch.Queue(`SELECT site_id FROM agents WHERE id = $1`, agentID)
	batch.Queue(`
		SELECT pc.id, pc.probe_type, pc.interval_ms, pc.timeout_ms, pc.train_count, pc.train_spacing_ms, pc.params,
		       t.id, t.kind, t.address, t.port, t.url
		FROM probe_configs pc
		JOIN targets t ON t.id = pc.target_id
		JOIN agents a ON a.site_id = pc.site_id
		WHERE a.id = $1 AND pc.enabled
		ORDER BY pc.created_at`, agentID)
	batch.Queue(`
		SELECT pc.mesh_id, pc.probe_type, pc.interval_ms, pc.timeout_ms, pc.train_count, pc.train_spacing_ms, pc.params
		FROM probe_configs pc
		JOIN mesh_members mm ON mm.mesh_id = pc.mesh_id
		JOIN agents a ON a.site_id = mm.site_id
		WHERE a.id = $1 AND pc.enabled
		ORDER BY pc.created_at`, agentID)
	batch.Queue(`
		SELECT DISTINCT mine.mesh_id, peer.id, peer.site_id, t.id, peer.probe_address
		FROM agents me
		JOIN mesh_members mine ON mine.site_id = me.site_id
		JOIN mesh_members theirs ON theirs.mesh_id = mine.mesh_id AND theirs.site_id <> mine.site_id
		JOIN agents peer ON peer.site_id = theirs.site_id
		JOIN targets t ON t.agent_id = peer.id
		WHERE me.id = $1`, agentID)

	res := s.pool.SendBatch(ctx, batch)
	defer res.Close()

	if err := res.QueryRow().Scan(&in.SiteID); err != nil {
		return in, fmt.Errorf("load config inputs: agent %s: %w", agentID, err)
	}

	rows, err := res.Query()
	if err != nil {
		return in, fmt.Errorf("load config inputs: direct probes: %w", err)
	}
	for rows.Next() {
		var (
			d                                     DirectProbeRow
			intervalMS, timeoutMS, trainSpacingMS int64
		)
		if err := rows.Scan(&d.ID, &d.Settings.ProbeType, &intervalMS, &timeoutMS,
			&d.Settings.TrainCount, &trainSpacingMS, &d.Settings.Params,
			&d.TargetID, &d.Kind, &d.Address, &d.Port, &d.URL); err != nil {
			rows.Close()
			return in, fmt.Errorf("load config inputs: direct probes: %w", err)
		}
		d.Settings.Interval = time.Duration(intervalMS) * time.Millisecond
		d.Settings.Timeout = time.Duration(timeoutMS) * time.Millisecond
		d.Settings.TrainSpacing = time.Duration(trainSpacingMS) * time.Millisecond
		in.Direct = append(in.Direct, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return in, fmt.Errorf("load config inputs: direct probes: %w", err)
	}

	rows, err = res.Query()
	if err != nil {
		return in, fmt.Errorf("load config inputs: mesh probes: %w", err)
	}
	for rows.Next() {
		var (
			m                                     MeshProbeRow
			intervalMS, timeoutMS, trainSpacingMS int64
		)
		if err := rows.Scan(&m.MeshID, &m.Settings.ProbeType, &intervalMS, &timeoutMS,
			&m.Settings.TrainCount, &trainSpacingMS, &m.Settings.Params); err != nil {
			rows.Close()
			return in, fmt.Errorf("load config inputs: mesh probes: %w", err)
		}
		m.Settings.Interval = time.Duration(intervalMS) * time.Millisecond
		m.Settings.Timeout = time.Duration(timeoutMS) * time.Millisecond
		m.Settings.TrainSpacing = time.Duration(trainSpacingMS) * time.Millisecond
		in.Mesh = append(in.Mesh, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return in, fmt.Errorf("load config inputs: mesh probes: %w", err)
	}

	rows, err = res.Query()
	if err != nil {
		return in, fmt.Errorf("load config inputs: peers: %w", err)
	}
	for rows.Next() {
		var p PeerRow
		if err := rows.Scan(&p.MeshID, &p.AgentID, &p.SiteID, &p.TargetID, &p.ProbeAddress); err != nil {
			rows.Close()
			return in, fmt.Errorf("load config inputs: peers: %w", err)
		}
		in.Peers = append(in.Peers, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return in, fmt.Errorf("load config inputs: peers: %w", err)
	}

	return in, nil
}
