package main

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/atripati/ark/pkg/supervise"
)

// BenchmarkSessionLifecycle measures one full check -> consume -> record lifecycle (in-memory
// store) end to end through the session, including fingerprinting and the mutex.
func BenchmarkSessionLifecycle(b *testing.B) {
	s := newExtSession(sessionCmd{Task: "bench", Supervision: "experimental", Budget: 4})
	ev := rankEvidenceRaw()
	act := &supervise.ProposedAction{Option: "B"}
	exec := true
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		txn := fmt.Sprintf("txn-%d", i)
		r := s.check(sessionCmd{Action: "tool_call", Tool: "book", Constraint: "rank",
			Scope: "order", TransactionID: txn, Proposed: supervise.ProposedAction{Option: "B"}, Evidence: ev})
		id := r["decision_id"].(string)
		s.consume(sessionCmd{Of: id, ExecutedAction: act})
		s.record(sessionCmd{Tool: "book", Of: id, Executed: &exec, ExecutedAction: act})
	}
}

// concurrentLifecycleThroughput drives `workers` goroutines through independent lifecycles for
// `dur` and returns completed lifecycles/sec. Used by TestPerfSmoke (not a hard assertion).
func concurrentLifecycleThroughput(workers int, dur time.Duration) float64 {
	s := newExtSession(sessionCmd{Task: "bench", Supervision: "experimental", Budget: 4})
	ev := rankEvidenceRaw()
	act := &supervise.ProposedAction{Option: "B"}
	var done int64
	var wg sync.WaitGroup
	deadline := time.Now().Add(dur)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			i := 0
			for time.Now().Before(deadline) {
				i++
				txn := fmt.Sprintf("w%d-%d", w, i)
				r := s.check(sessionCmd{Action: "tool_call", Tool: "book", Constraint: "rank",
					Scope: "order", TransactionID: txn, Proposed: supervise.ProposedAction{Option: "B"}, Evidence: ev})
				id, _ := r["decision_id"].(string)
				if id == "" {
					continue
				}
				s.consume(sessionCmd{Of: id, ExecutedAction: act})
				exec := true
				s.record(sessionCmd{Tool: "book", Of: id, Executed: &exec, ExecutedAction: act})
				atomic.AddInt64(&done, 1)
			}
		}(w)
	}
	wg.Wait()
	return float64(atomic.LoadInt64(&done)) / dur.Seconds()
}

// TestPerfSmoke prints IN-MEMORY throughput at a few concurrency levels (run with -v). It only
// asserts the system makes progress — it is a smoke signal, not a hard SLO.
func TestPerfSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("perf smoke skipped in -short")
	}
	for _, workers := range []int{1, 10, 100} {
		tput := concurrentLifecycleThroughput(workers, 250*time.Millisecond)
		t.Logf("[in-memory] workers=%-4d  lifecycles/sec=%.0f", workers, tput)
		if tput <= 0 {
			t.Fatalf("no progress at workers=%d", workers)
		}
	}
}

// TestPerfSmokeDurable prints DURABLE-MODE throughput (fsync-bound; NOT comparable to in-memory).
func TestPerfSmokeDurable(t *testing.T) {
	if testing.Short() {
		t.Skip("perf smoke skipped in -short")
	}
	t.Setenv("ARK_AUTHZ_DIR", t.TempDir())
	for _, workers := range []int{1, 10, 50} {
		tput := concurrentLifecycleThroughput(workers, 400*time.Millisecond)
		t.Logf("[DURABLE fsync] workers=%-4d  lifecycles/sec=%.0f", workers, tput)
		if tput <= 0 {
			t.Fatalf("no progress at workers=%d (durable)", workers)
		}
	}
}
