package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// TargetInfo is a targets row as shown by the admin CLI.
type TargetInfo struct {
	ID        uuid.UUID
	Kind      string
	Name      string
	AgentID   *uuid.UUID
	Address   string
	Port      int32
	URL       string
	CreatedAt time.Time
}

// MeshGroupInfo is a mesh group with its member site names.
type MeshGroupInfo struct {
	ID    uuid.UUID
	Name  string
	Sites []string
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
		return uuid.Nil, fmt.Errorf("target %q already exists as an agent target", name)
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("upsert target %q: %w", name, err)
	}
	return id, nil
}

// ListTargets returns all targets, agents included.
func (s *Store) ListTargets(ctx context.Context) ([]TargetInfo, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, kind, name, agent_id, address, port, url, created_at
		FROM targets ORDER BY kind, name`)
	if err != nil {
		return nil, fmt.Errorf("list targets: %w", err)
	}
	defer rows.Close()
	var out []TargetInfo
	for rows.Next() {
		var t TargetInfo
		if err := rows.Scan(&t.ID, &t.Kind, &t.Name, &t.AgentID, &t.Address, &t.Port, &t.URL, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("list targets: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteTarget removes an external target by name. Agent targets cannot be
// deleted (they go away with the agent), and targets referenced by probe
// configs are protected by the FK — both fail loudly.
func (s *Store) DeleteTarget(ctx context.Context, name string) error {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM targets WHERE name = $1 AND kind = 'external'`, name)
	if err != nil {
		return fmt.Errorf("delete target %q (still referenced by a probe config?): %w", name, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("external target %q does not exist", name)
	}
	return nil
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
		return uuid.Nil, fmt.Errorf("site %q does not exist", name)
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
		return uuid.Nil, fmt.Errorf("mesh group %q does not exist", name)
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
		return uuid.Nil, fmt.Errorf("target %q does not exist", name)
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

// RemoveMeshMember removes a site from a mesh group.
func (s *Store) RemoveMeshMember(ctx context.Context, meshName, siteName string) error {
	meshID, err := s.meshIDByName(ctx, meshName)
	if err != nil {
		return err
	}
	siteID, err := s.SiteIDByName(ctx, siteName)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM mesh_members WHERE mesh_id = $1 AND site_id = $2`, meshID, siteID)
	if err != nil {
		return fmt.Errorf("remove %q from mesh %q: %w", siteName, meshName, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("site %q is not a member of mesh %q", siteName, meshName)
	}
	return nil
}

// ListMeshGroups returns all mesh groups with their member site names.
func (s *Store) ListMeshGroups(ctx context.Context) ([]MeshGroupInfo, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT g.id, g.name, COALESCE(array_agg(s.name ORDER BY s.name) FILTER (WHERE s.name IS NOT NULL), '{}')
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
		if err := rows.Scan(&g.ID, &g.Name, &g.Sites); err != nil {
			return nil, fmt.Errorf("list mesh groups: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// AddDirectProbe assigns a probe of target to every agent at site.
func (s *Store) AddDirectProbe(ctx context.Context, siteName, targetName string, ps ProbeSettings) (uuid.UUID, error) {
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
		INSERT INTO probe_configs (site_id, target_id, probe_type, interval_ms, timeout_ms, train_count, train_spacing_ms, params)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id`,
		siteID, targetID, ps.ProbeType, ps.Interval.Milliseconds(), ps.Timeout.Milliseconds(),
		ps.TrainCount, ps.TrainSpacing.Milliseconds(), ps.Params).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("add probe: %w", err)
	}
	return id, nil
}

// AddMeshProbe creates a mesh probe template expanded over ordered site pairs.
func (s *Store) AddMeshProbe(ctx context.Context, meshName string, ps ProbeSettings) (uuid.UUID, error) {
	meshID, err := s.meshIDByName(ctx, meshName)
	if err != nil {
		return uuid.Nil, err
	}
	var id uuid.UUID
	err = s.pool.QueryRow(ctx, `
		INSERT INTO probe_configs (mesh_id, probe_type, interval_ms, timeout_ms, train_count, train_spacing_ms, params)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id`,
		meshID, ps.ProbeType, ps.Interval.Milliseconds(), ps.Timeout.Milliseconds(),
		ps.TrainCount, ps.TrainSpacing.Milliseconds(), ps.Params).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("add mesh probe: %w", err)
	}
	return id, nil
}

// ListProbeConfigs returns every probe config with names resolved.
func (s *Store) ListProbeConfigs(ctx context.Context) ([]ProbeConfigInfo, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT pc.id, COALESCE(s.name, ''), COALESCE(t.name, ''), COALESCE(g.name, ''),
		       pc.probe_type, pc.interval_ms, pc.timeout_ms, pc.train_count, pc.train_spacing_ms,
		       pc.params, pc.enabled
		FROM probe_configs pc
		LEFT JOIN sites s ON s.id = pc.site_id
		LEFT JOIN targets t ON t.id = pc.target_id
		LEFT JOIN mesh_groups g ON g.id = pc.mesh_id
		ORDER BY pc.created_at`)
	if err != nil {
		return nil, fmt.Errorf("list probe configs: %w", err)
	}
	defer rows.Close()
	var out []ProbeConfigInfo
	for rows.Next() {
		var (
			p                                    ProbeConfigInfo
			intervalMS, timeoutMS, trainSpacingMS int64
		)
		if err := rows.Scan(&p.ID, &p.Site, &p.Target, &p.Mesh, &p.ProbeType,
			&intervalMS, &timeoutMS, &p.TrainCount, &trainSpacingMS, &p.Params, &p.Enabled); err != nil {
			return nil, fmt.Errorf("list probe configs: %w", err)
		}
		p.Interval = time.Duration(intervalMS) * time.Millisecond
		p.Timeout = time.Duration(timeoutMS) * time.Millisecond
		p.TrainSpacing = time.Duration(trainSpacingMS) * time.Millisecond
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeleteProbeConfig removes a probe config by ID.
func (s *Store) DeleteProbeConfig(ctx context.Context, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM probe_configs WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete probe config: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("probe config %s does not exist", id)
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
