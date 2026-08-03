// Package probeid derives the stable probe IDs shared by config expansion
// (meshexpand) and admin mutations (store cleanup of expanded series). It is
// a leaf package — meshexpand imports store, so store cannot reach the
// derivation through meshexpand without a cycle.
package probeid

import (
	"fmt"

	"github.com/google/uuid"
)

// MeshProbeID is the architecture's UUIDv5(mesh, "src|dst|type"): stable
// across rebuilds, distinct per direction (A→B ≠ B→A) and per probe type.
// The derivation is frozen — these IDs are stored in probe_results, so any
// change would orphan all mesh history.
func MeshProbeID(meshID, srcSite, dstSite uuid.UUID, probeType int16) uuid.UUID {
	name := fmt.Sprintf("%s|%s|%d", srcSite, dstSite, probeType)
	return uuid.NewSHA1(meshID, []byte(name))
}
