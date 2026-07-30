// PushResults ingestion: wire → row mapping and target-ownership checks.
package grpcapi

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	pb "github.com/devalexllc/lighthouse/internal/pb/lighthousev1"
	"github.com/devalexllc/lighthouse/internal/server/outage"
	"github.com/devalexllc/lighthouse/internal/server/store"
)

const (
	// Sanity cap well above the agent's ≤500 batch size; anything larger is
	// a broken or hostile client.
	maxBatchSize = 5000
	// Results stamped further in the future than this are clock garbage.
	maxFutureSkew = 5 * time.Minute
	// error text is truncated to keep hypertable rows narrow.
	maxErrorLen = 128

	ownershipCacheTTL = 30 * time.Second
)

// ownershipCache memoizes TargetAssignedToAgent so a batch touching a
// handful of targets costs a handful of queries. Only positive AND negative
// answers within the TTL are trusted — assignment changes converge within
// 30 s, same as config distribution.
type ownershipCache struct {
	mu      sync.Mutex
	entries map[[32]byte]ownershipEntry
}

type ownershipEntry struct {
	ok      bool
	expires time.Time
}

func cacheKey(agentID, targetID uuid.UUID) [32]byte {
	var k [32]byte
	copy(k[:16], agentID[:])
	copy(k[16:], targetID[:])
	return k
}

func (c *ownershipCache) lookup(agentID, targetID uuid.UUID, now time.Time) (ok, hit bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, found := c.entries[cacheKey(agentID, targetID)]
	if !found || now.After(e.expires) {
		return false, false
	}
	return e.ok, true
}

func (c *ownershipCache) put(agentID, targetID uuid.UUID, ok bool, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[[32]byte]ownershipEntry)
	}
	// Opportunistic sweep keeps the map bounded without a background task.
	if len(c.entries) > 4096 {
		for k, e := range c.entries {
			if now.After(e.expires) {
				delete(c.entries, k)
			}
		}
	}
	c.entries[cacheKey(agentID, targetID)] = ownershipEntry{ok: ok, expires: now.Add(ownershipCacheTTL)}
}

func (s *Server) targetAssigned(ctx context.Context, agentID, targetID uuid.UUID) (bool, error) {
	now := time.Now()
	if ok, hit := s.ownership.lookup(agentID, targetID, now); hit {
		return ok, nil
	}
	ok, err := s.store.TargetAssignedToAgent(ctx, agentID, targetID)
	if err != nil {
		return false, err
	}
	s.ownership.put(agentID, targetID, ok, now)
	return ok, nil
}

// resultToRow maps one wire ProbeResult to a hypertable row. Pure: all
// validation failures return an error naming the problem. now is injected
// for testability.
func resultToRow(r *pb.ProbeResult, now time.Time) (store.ResultRow, error) {
	var row store.ResultRow

	probeID, err := uuid.Parse(r.GetProbeId())
	if err != nil {
		return row, fmt.Errorf("bad probe_id %q", r.GetProbeId())
	}
	targetID, err := uuid.Parse(r.GetTargetId())
	if err != nil {
		return row, fmt.Errorf("bad target_id %q", r.GetTargetId())
	}
	if r.GetStartedAt() == nil {
		return row, fmt.Errorf("missing started_at")
	}
	t := r.GetStartedAt().AsTime()
	if t.After(now.Add(maxFutureSkew)) {
		return row, fmt.Errorf("started_at %s is in the future", t.Format(time.RFC3339))
	}
	if r.GetStatus() == pb.ProbeStatus_PROBE_STATUS_UNSPECIFIED {
		return row, fmt.Errorf("status unspecified")
	}

	row = store.ResultRow{
		Time:      t,
		TargetID:  targetID,
		ProbeID:   probeID,
		ProbeType: int16(r.GetType()),
		Status:    int16(r.GetStatus()),
		Sent:      int32(min(r.GetSent(), 1<<30)),
		Received:  int32(min(r.GetReceived(), 1<<30)),
	}
	if row.Sent > 0 {
		loss := float32(row.Sent-row.Received) / float32(row.Sent) * 100
		row.LossPct = &loss
	}
	if rtt := r.GetRtt(); rtt != nil {
		row.RttMinUS = usColumn(rtt.GetMinUs())
		row.RttAvgUS = usColumn(rtt.GetAvgUs())
		row.RttMaxUS = usColumn(rtt.GetMaxUs())
		row.RttStddevUS = usColumn(rtt.GetStddevUs())
	}
	row.JitterUS = usColumn(r.GetJitterUs())
	if tm := r.GetTimings(); tm != nil {
		row.DNSUS = usColumn(tm.GetDnsUs())
		row.TCPConnectUS = usColumn(tm.GetTcpConnectUs())
		row.TLSHandshakeUS = usColumn(tm.GetTlsHandshakeUs())
		row.TTFBUS = usColumn(tm.GetTtfbUs())
		row.TotalUS = usColumn(tm.GetTotalUs())
	}
	if e := r.GetError(); e != "" {
		if len(e) > maxErrorLen {
			e = e[:maxErrorLen]
		}
		row.Error = &e
	}
	return row, nil
}

// toOutageResults maps genuinely inserted rows to the outage package's
// input. UNSUPPORTED and every other non-OK status count as failures.
func toOutageResults(rows []store.ResultRow) []outage.Result {
	out := make([]outage.Result, len(rows))
	for i, r := range rows {
		var errText string
		if r.Error != nil {
			errText = *r.Error
		}
		out[i] = outage.Result{
			ProbeID:    r.ProbeID,
			TargetID:   r.TargetID,
			ProbeType:  r.ProbeType,
			Time:       r.Time,
			OK:         r.Status == int16(pb.ProbeStatus_PROBE_STATUS_OK),
			StatusCode: r.Status,
			Error:      errText,
		}
	}
	return out
}

// usColumn converts a wire microsecond value to a nullable column: negative
// means "not measured" (NULL), and values beyond int32 are clamped (an int32
// of microseconds is ~35 minutes — far past any probe timeout).
func usColumn(us int64) *int32 {
	if us < 0 {
		return nil
	}
	v := int32(min(us, 1<<31-1))
	return &v
}
