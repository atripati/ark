package main

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/atripati/ark/pkg/authz"
	"github.com/atripati/ark/pkg/supervise"
)

// authState returns the durable authorization state for an in-session decision id.
func authState(t *testing.T, s *extSession, decisionID string) authz.State {
	t.Helper()
	r, err := s.store.Get(s.authByDecision[decisionID])
	if err != nil {
		t.Fatalf("store.Get for %s: %v", decisionID, err)
	}
	return r.State
}

// rankEvidenceWithMeta is rank evidence carrying freshness metadata (observed_at / expires_at).
func rankEvidenceWithMeta(observedAt, expiresAt int64) json.RawMessage {
	b, _ := json.Marshal(supervise.Evidence{
		RequestedRank: 2, EvidenceComplete: true,
		Options: []supervise.Option{
			{ID: "A", Price: 163, IsDirect: true},
			{ID: "B", Price: 290, IsDirect: true},
		},
		Meta: supervise.EvidenceMeta{ObservedAtUnix: observedAt, ExpiresAtUnix: expiresAt, Source: "inventory-svc", Version: "v7"},
	})
	return b
}

func allowB(t *testing.T, s *extSession, txn string) string {
	t.Helper()
	r := s.check(sessionCmd{Action: "tool_call", Tool: "book", Constraint: "rank",
		Scope: "order-1", TransactionID: txn, Proposed: supervise.ProposedAction{Option: "B"},
		Evidence: rankEvidenceRaw()})
	if r["verdict"] != "ALLOW" {
		t.Fatalf("expected ALLOW, got %v", r)
	}
	return r["decision_id"].(string)
}

// ---- Phase 1: pre-execution consume gate ----

func TestConsumeThenExecuteThenRecord(t *testing.T) {
	s := expSession()
	id := allowB(t, s, "txn-1")
	act := &supervise.ProposedAction{Option: "B"}
	cr := s.consume(sessionCmd{Of: id, ExecutedAction: act})
	if cr["cleared"] != true {
		t.Fatalf("consume must clear a valid fresh authorization; got %v", cr)
	}
	if cr["idempotency_key"] == nil || cr["idempotency_key"] == "" {
		t.Fatalf("consume must return an idempotency_key; got %v", cr)
	}
	exec := true
	rr := s.record(sessionCmd{Tool: "book", Of: id, Executed: &exec, ExecutedAction: act})
	if rr["error"] != nil {
		t.Fatalf("record after consume must succeed; got %v", rr)
	}
	if st := authState(t, s, id); st != authz.Completed {
		t.Fatalf("authorization must be COMPLETED after record; got %s", st)
	}
}

// ATTACK 1: fresh at check, stale at execution time -> consume blocks BEFORE the side effect.
func TestConsumeBlocksStaleAuthorization(t *testing.T) {
	s := expSession()
	now := time.Now().Unix()
	// fresh at check (a 1s window), so it goes stale before we consume.
	r := s.check(sessionCmd{Action: "tool_call", Tool: "book", Constraint: "rank", Scope: "order-1",
		TransactionID: "txn-1", Proposed: supervise.ProposedAction{Option: "B"},
		Evidence: rankEvidenceWithMeta(now, now+1)})
	if r["verdict"] != "ALLOW" {
		t.Fatalf("fresh evidence must ALLOW at check; got %v", r)
	}
	id := r["decision_id"].(string)
	time.Sleep(1200 * time.Millisecond) // the freshness window elapses before execution
	cr := s.consume(sessionCmd{Of: id, ExecutedAction: &supervise.ProposedAction{Option: "B"}})
	if cr["cleared"] != false || cr["requires_recheck"] != true {
		t.Fatalf("a stale authorization must NOT clear at the pre-execution gate; got %v", cr)
	}
	// and it must NOT have been consumed, so nothing executed on a stale authorization
	if st := authState(t, s, id); st != authz.Issued {
		t.Fatalf("a blocked (stale) authorization must remain unconsumed; got %s", st)
	}
}

func TestConsumeRefusesNonAllow(t *testing.T) {
	s := expSession()
	r := s.check(sessionCmd{Action: "tool_call", Tool: "book", Constraint: "rank", Scope: "order-1",
		TransactionID: "txn-1", Proposed: supervise.ProposedAction{Option: "A"}, // rank-1 -> REJECT
		Evidence: rankEvidenceRaw()})
	id := r["decision_id"].(string)
	cr := s.consume(sessionCmd{Of: id, ExecutedAction: &supervise.ProposedAction{Option: "A"}})
	if cr["error"] == nil {
		t.Fatalf("consuming a non-ALLOW decision must be refused; got %v", cr)
	}
}

func TestConsumeRefusesActionMismatch(t *testing.T) {
	s := expSession()
	id := allowB(t, s, "txn-1")
	cr := s.consume(sessionCmd{Of: id, ExecutedAction: &supervise.ProposedAction{Option: "A"}}) // checked B
	if cr["error"] == nil {
		t.Fatalf("consuming a different action than authorized must be refused; got %v", cr)
	}
}

func TestConsumeRefusesMissingAction(t *testing.T) {
	s := expSession()
	id := allowB(t, s, "txn-1")
	cr := s.consume(sessionCmd{Of: id}) // no ExecutedAction
	if cr["error"] == nil {
		t.Fatalf("consume without executed_action must be refused; got %v", cr)
	}
}

// ATTACK: consume the same authorization twice -> exactly one succeeds (replay refused).
func TestConsumeReplayRefused(t *testing.T) {
	s := expSession()
	id := allowB(t, s, "txn-1")
	act := &supervise.ProposedAction{Option: "B"}
	if cr := s.consume(sessionCmd{Of: id, ExecutedAction: act}); cr["cleared"] != true {
		t.Fatalf("first consume must clear; got %v", cr)
	}
	cr2 := s.consume(sessionCmd{Of: id, ExecutedAction: act})
	if cr2["error"] == nil {
		t.Fatalf("second consume of the same authorization must be refused (replay); got %v", cr2)
	}
}

// ATTACK 4: concurrent double-consume -> exactly ONE consumption succeeds.
func TestConcurrentDoubleConsumeExactlyOne(t *testing.T) {
	s := expSession()
	id := allowB(t, s, "txn-1")
	act := &supervise.ProposedAction{Option: "B"}
	var wg sync.WaitGroup
	var mu sync.Mutex
	cleared := 0
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cr := s.consume(sessionCmd{Of: id, ExecutedAction: act})
			if cr["cleared"] == true {
				mu.Lock()
				cleared++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if cleared != 1 {
		t.Fatalf("exactly one concurrent consume must clear; got %d", cleared)
	}
}

// ATTACK 6: replay across a bridge restart. Authorization state is in-memory per session, so a
// NEW session has no memory of a prior authorization — a replay is refused as UNKNOWN (fail
// closed), never silently re-cleared. This documents the exact boundary: in-process only.
func TestCrossSessionReplayRefusedAsUnknown(t *testing.T) {
	s1 := expSession()
	id := allowB(t, s1, "txn-1")
	act := &supervise.ProposedAction{Option: "B"}
	if cr := s1.consume(sessionCmd{Of: id, ExecutedAction: act}); cr["cleared"] != true {
		t.Fatalf("consume in session 1 must clear; got %v", cr)
	}
	// simulate a bridge restart / new process: a brand-new session
	s2 := expSession()
	cr := s2.consume(sessionCmd{Of: id, ExecutedAction: act})
	if cr["error"] == nil {
		t.Fatalf("a pre-restart authorization must be UNKNOWN to a new session (fail closed), never re-cleared; got %v", cr)
	}
}

// Two authorizations sharing (transaction, action, evidence) but differing in SCOPE or NAMESPACE
// must NOT alias — each gets its own id and its own single-use lifecycle (DUR-03).
func TestAuthorizationDoesNotAliasAcrossScopeOrTenant(t *testing.T) {
	s := expSession()
	mk := func(scope, ns string) map[string]any {
		return s.check(sessionCmd{Action: "tool_call", Tool: "book", Constraint: "rank",
			Scope: scope, Namespace: ns, TransactionID: "refund-1", // SAME transaction on purpose
			Proposed: supervise.ProposedAction{Option: "B"}, Evidence: rankEvidenceRaw()})
	}
	a := mk("customer-A", "")
	b := mk("customer-B", "")          // same txn/action/evidence, different scope
	c := mk("customer-A", "tenant-2")  // same as a but different tenant/namespace
	idA := a["authorization_id"].(string)
	idB := b["authorization_id"].(string)
	idC := c["authorization_id"].(string)
	if idA == idB || idA == idC || idB == idC {
		t.Fatalf("authorizations must not alias across scope/tenant; got A=%s B=%s C=%s", idA, idB, idC)
	}
	// consuming A must not consume B — each is independent and single-use
	act := &supervise.ProposedAction{Option: "B"}
	if cr := s.consume(sessionCmd{AuthorizationID: idA, ExecutedAction: act}); cr["cleared"] != true {
		t.Fatalf("consume A must clear; got %v", cr)
	}
	if cr := s.consume(sessionCmd{AuthorizationID: idB, ExecutedAction: act}); cr["cleared"] != true {
		t.Fatalf("consume B must clear independently (A did not consume it); got %v", cr)
	}
}

// ---- Phase 3: transaction identity isolates retry state ----

// ATTACK 3: same entity, two DISTINCT transactions -> independent retry budgets.
func TestTransactionIsolatesRetryBudget(t *testing.T) {
	s := expSession() // budget 3
	// exhaust transaction A (rank-1 always REJECTs), same scope/customer
	seen := []string{}
	for i := 0; i < 5; i++ {
		r := s.check(sessionCmd{Constraint: "rank", Scope: "customer-123", TransactionID: "txn-A",
			Proposed: supervise.ProposedAction{Option: "A"}, Evidence: rankEvidenceRaw()})
		seen = append(seen, r["verdict"].(string))
		if r["verdict"] == "RECOVERY_EXHAUSTED" {
			break
		}
	}
	if seen[len(seen)-1] != "RECOVERY_EXHAUSTED" {
		t.Fatalf("txn-A should have exhausted; got %v", seen)
	}
	// a DIFFERENT transaction for the SAME customer must start fresh
	rb := s.check(sessionCmd{Constraint: "rank", Scope: "customer-123", TransactionID: "txn-B",
		Proposed: supervise.ProposedAction{Option: "A"}, Evidence: rankEvidenceRaw()})
	if rb["verdict"] != "REJECT" {
		t.Fatalf("INV-07: txn-B for the same customer inherited txn-A's exhausted budget; got %v", rb["verdict"])
	}
}

// ---- Phase 6: concurrency — many transactions, no contamination, no races (run under -race) ----

func TestConcurrentTransactionsNoContamination(t *testing.T) {
	s := newExtSession(sessionCmd{Task: "load", Supervision: "experimental", Budget: 4})
	const N = 100
	var wg sync.WaitGroup
	errs := make(chan string, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			txn := fmt.Sprintf("txn-%d", i)
			r := s.check(sessionCmd{Action: "tool_call", Tool: "book", Constraint: "rank",
				Scope: fmt.Sprintf("order-%d", i), TransactionID: txn, AgentID: fmt.Sprintf("agent-%d", i%7),
				Proposed: supervise.ProposedAction{Option: "B"}, Evidence: rankEvidenceRaw()})
			if r["verdict"] != "ALLOW" {
				errs <- fmt.Sprintf("txn %s: expected ALLOW got %v", txn, r["verdict"])
				return
			}
			id := r["decision_id"].(string)
			act := &supervise.ProposedAction{Option: "B"}
			if cr := s.consume(sessionCmd{Of: id, ExecutedAction: act}); cr["cleared"] != true {
				errs <- fmt.Sprintf("txn %s: consume not cleared: %v", txn, cr)
				return
			}
			exec := true
			if rr := s.record(sessionCmd{Tool: "book", Of: id, Executed: &exec, ExecutedAction: act}); rr["error"] != nil {
				errs <- fmt.Sprintf("txn %s: record error %v", txn, rr)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
	// every one of the N transactions produced exactly one completed authorization
	completed := 0
	for did := range s.authByDecision {
		if authState(t, s, did) == authz.Completed {
			completed++
		}
	}
	if completed != N {
		t.Fatalf("expected %d completed authorizations, got %d", N, completed)
	}
}
