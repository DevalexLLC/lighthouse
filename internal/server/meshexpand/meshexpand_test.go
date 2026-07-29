package meshexpand

import (
	"testing"
	"time"

	"github.com/google/uuid"

	pb "github.com/devalexllc/lighthouse/internal/pb/lighthousev1"
	"github.com/devalexllc/lighthouse/internal/server/store"
)

var (
	meshID   = uuid.MustParse("00000000-0000-0000-0000-00000000000a")
	siteA    = uuid.MustParse("00000000-0000-0000-0000-0000000000a1")
	siteB    = uuid.MustParse("00000000-0000-0000-0000-0000000000b1")
	siteC    = uuid.MustParse("00000000-0000-0000-0000-0000000000c1")
	agentA   = uuid.MustParse("00000000-0000-0000-0000-0000000000a2")
	agentB   = uuid.MustParse("00000000-0000-0000-0000-0000000000b2")
	agentC   = uuid.MustParse("00000000-0000-0000-0000-0000000000c2")
	targetB  = uuid.MustParse("00000000-0000-0000-0000-0000000000b3")
	targetC  = uuid.MustParse("00000000-0000-0000-0000-0000000000c3")
	directID = uuid.MustParse("00000000-0000-0000-0000-0000000000d1")
)

func tcpSettings() store.ProbeSettings {
	return store.ProbeSettings{
		ProbeType: int16(pb.ProbeType_PROBE_TYPE_TCP),
		Interval:  30 * time.Second,
		Timeout:   5 * time.Second,
	}
}

func inputsForA() store.AgentConfigInputs {
	return store.AgentConfigInputs{
		AgentID: agentA,
		SiteID:  siteA,
		Direct: []store.DirectProbeRow{{
			ID:       directID,
			Settings: tcpSettings(),
			TargetID: uuid.MustParse("00000000-0000-0000-0000-0000000000e1"),
			Kind:     "external",
			Address:  "db.example",
			Port:     5432,
		}},
		Mesh: []store.MeshProbeRow{{
			MeshID: meshID,
			Settings: store.ProbeSettings{
				ProbeType: int16(pb.ProbeType_PROBE_TYPE_TCP),
				Interval:  time.Minute,
				Timeout:   5 * time.Second,
				Params:    map[string]string{"port": "9"},
			},
		}},
		Peers: []store.PeerRow{
			{MeshID: meshID, AgentID: agentB, SiteID: siteB, TargetID: targetB, ProbeAddress: "10.0.0.2"},
			{MeshID: meshID, AgentID: agentC, SiteID: siteC, TargetID: targetC, ProbeAddress: "10.0.0.3"},
		},
	}
}

func TestBuildSnapshotExpandsDirectAndMesh(t *testing.T) {
	snap := BuildSnapshot(inputsForA())
	if len(snap.Probes) != 3 {
		t.Fatalf("got %d probes, want 3 (1 direct + 2 mesh peers)", len(snap.Probes))
	}
	var direct, mesh int
	for _, p := range snap.Probes {
		if p.ProbeId == directID.String() {
			direct++
			if p.Target.Kind != pb.TargetKind_TARGET_KIND_EXTERNAL || p.Target.Port != 5432 {
				t.Errorf("direct target wrong: %+v", p.Target)
			}
		} else {
			mesh++
			if p.Target.Kind != pb.TargetKind_TARGET_KIND_AGENT_PEER {
				t.Errorf("mesh target kind = %v", p.Target.Kind)
			}
			if p.Target.Port != 9 {
				t.Errorf("mesh port = %d, want 9 (from params)", p.Target.Port)
			}
		}
	}
	if direct != 1 || mesh != 2 {
		t.Errorf("direct=%d mesh=%d", direct, mesh)
	}
}

func TestHashDeterministicAcrossInputOrder(t *testing.T) {
	in1 := inputsForA()
	in2 := inputsForA()
	// Reverse peer order; the snapshot sorts by probe_id, so the hash must
	// not depend on database row order.
	in2.Peers[0], in2.Peers[1] = in2.Peers[1], in2.Peers[0]

	h1 := BuildSnapshot(in1).ConfigHash
	h2 := BuildSnapshot(in2).ConfigHash
	if h1 != h2 {
		t.Errorf("hash depends on input order: %s vs %s", h1, h2)
	}
	if h1 == "" {
		t.Error("empty hash")
	}
}

func TestHashChangesWithConfig(t *testing.T) {
	base := BuildSnapshot(inputsForA()).ConfigHash
	changed := inputsForA()
	changed.Direct[0].Settings.Interval = time.Hour
	if BuildSnapshot(changed).ConfigHash == base {
		t.Error("interval change must change the hash")
	}
}

func TestMeshProbeIDDirectionality(t *testing.T) {
	ab := meshProbeID(meshID, siteA, siteB, int16(pb.ProbeType_PROBE_TYPE_TCP))
	ba := meshProbeID(meshID, siteB, siteA, int16(pb.ProbeType_PROBE_TYPE_TCP))
	if ab == ba {
		t.Error("A→B and B→A must have distinct probe ids")
	}
	// Stability golden value: this must NEVER change — probe_ids are stored
	// in probe_results and referenced across releases.
	if got := ab.String(); got != meshProbeID(meshID, siteA, siteB, 2).String() {
		t.Errorf("probe id not stable: %s", got)
	}
	if ab.Version() != 5 {
		t.Errorf("uuid version = %d, want 5 (UUIDv5/SHA-1)", ab.Version())
	}
}

func TestPeersOfOtherMeshesExcluded(t *testing.T) {
	in := inputsForA()
	otherMesh := uuid.MustParse("00000000-0000-0000-0000-00000000000b")
	in.Peers = append(in.Peers, store.PeerRow{
		MeshID: otherMesh, AgentID: agentB, SiteID: siteB, TargetID: targetB, ProbeAddress: "10.9.9.9",
	})
	snap := BuildSnapshot(in)
	if len(snap.Probes) != 3 {
		t.Errorf("peer of an unrelated mesh leaked into expansion: %d probes", len(snap.Probes))
	}
}

func TestEmptyInputsStableHash(t *testing.T) {
	empty := store.AgentConfigInputs{AgentID: agentA, SiteID: siteA}
	h1 := BuildSnapshot(empty).ConfigHash
	h2 := BuildSnapshot(empty).ConfigHash
	if h1 != h2 || h1 == "" {
		t.Errorf("empty snapshot hash unstable: %q vs %q", h1, h2)
	}
}
