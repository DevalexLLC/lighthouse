package uplink

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/devalexllc/lighthouse/internal/agent/spool"
	pb "github.com/devalexllc/lighthouse/internal/pb/lighthousev1"
)

type fakeServer struct {
	mu      sync.Mutex
	batches [][]*pb.ProbeResult
	dropped []uint64
	failing bool
}

func (f *fakeServer) push(_ context.Context, results []*pb.ProbeResult, dropped uint64) (uint32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failing {
		return 0, errors.New("server unavailable")
	}
	batch := make([]*pb.ProbeResult, len(results))
	copy(batch, results)
	f.batches = append(f.batches, batch)
	f.dropped = append(f.dropped, dropped)
	return uint32(len(results)), nil
}

func (f *fakeServer) setFailing(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failing = v
}

func (f *fakeServer) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, b := range f.batches {
		n += len(b)
	}
	return n
}

func testSpool(t *testing.T) *spool.Spool {
	t.Helper()
	sp, err := spool.Open(t.TempDir(), 1<<30, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sp.Close() })
	return sp
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(msg)
}

func res(i int) *pb.ProbeResult {
	return &pb.ProbeResult{ProbeId: fmt.Sprintf("p-%04d", i), Status: pb.ProbeStatus_PROBE_STATUS_OK}
}

func TestPusherDrainsSpool(t *testing.T) {
	sp := testSpool(t)
	srv := &fakeServer{}
	p := &Pusher{sp: sp, push: srv.push}

	go p.Run(t.Context())

	for i := range 10 {
		if err := sp.Append(res(i)); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, 10*time.Second, func() bool { return srv.total() == 10 }, "pusher did not drain the spool")
	waitFor(t, 5*time.Second, func() bool { return sp.Pending() == 0 }, "pushed results were not acked")
}

func TestPusherRetriesAndBurstDrains(t *testing.T) {
	sp := testSpool(t)
	srv := &fakeServer{}
	srv.setFailing(true)
	p := &Pusher{sp: sp, push: srv.push}

	go p.Run(t.Context())

	// Outage: results accumulate, pushes fail, nothing is lost.
	for i := range 1200 {
		if err := sp.Append(res(i)); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(100 * time.Millisecond)
	if srv.total() != 0 {
		t.Fatal("results delivered while server failing")
	}
	if sp.Pending() != 1200 {
		t.Fatalf("pending = %d, want 1200", sp.Pending())
	}

	// Recovery: burst-drain in ≤500 batches, in order, exactly once.
	srv.setFailing(false)
	waitFor(t, 30*time.Second, func() bool { return srv.total() == 1200 }, "burst drain incomplete")
	srv.mu.Lock()
	defer srv.mu.Unlock()
	seen := 0
	for _, b := range srv.batches {
		if len(b) > batchSize {
			t.Errorf("batch of %d exceeds limit %d", len(b), batchSize)
		}
		for _, r := range b {
			if r.ProbeId != fmt.Sprintf("p-%04d", seen) {
				t.Fatalf("result %d out of order or duplicated: %s", seen, r.ProbeId)
			}
			seen++
		}
	}
}

func TestPusherReportsAndClearsDropped(t *testing.T) {
	// A tiny spool that has already overflowed reports its loss with the
	// next successful push, then clears the counter.
	sp, err := spool.Open(t.TempDir(), 4096, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()
	big := &pb.ProbeResult{ProbeId: "p", Error: string(make([]byte, 512))}
	for range 40 {
		if err := sp.Append(big); err != nil {
			t.Fatal(err)
		}
	}
	if sp.Dropped() == 0 {
		t.Fatal("precondition: spool must have overflowed")
	}

	srv := &fakeServer{}
	p := &Pusher{sp: sp, push: srv.push}
	go p.Run(t.Context())

	// Loss is reported alongside the NEXT successful push — sustained
	// overflow may have emptied the spool, so generate fresh traffic.
	for i := range 3 {
		if err := sp.Append(res(i)); err != nil {
			t.Fatal(err)
		}
	}

	waitFor(t, 10*time.Second, func() bool {
		srv.mu.Lock()
		defer srv.mu.Unlock()
		return len(srv.dropped) > 0 && srv.dropped[0] > 0
	}, "dropped count was not reported")
	waitFor(t, 5*time.Second, func() bool { return sp.Dropped() == 0 }, "dropped counter not cleared after report")
}
