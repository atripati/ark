package main

import (
	"encoding/json"
	"testing"

	"github.com/atripati/ark/pkg/supervise"
)

func expSession() *extSession {
	return newExtSession(sessionCmd{Task: "book", Supervision: "experimental", Budget: 3})
}

// ---- Phase 2: strict input validation / fail-closed configuration ----

func TestSessionCheckRequiresScope(t *testing.T) {
	s := expSession()
	r := s.check(sessionCmd{Constraint: "rank", Proposed: supervise.ProposedAction{Option: "A"},
		Evidence: rankEvidenceRaw()}) // no scope
	if r["error"] == nil {
		t.Fatalf("a check with no scope must fail closed; got %v", r)
	}
}

func TestSessionUnknownConstraintFailsClosed(t *testing.T) {
	s := expSession()
	r := s.check(sessionCmd{Constraint: "refund_limit", Scope: "txn-1",
		Proposed: supervise.ProposedAction{Option: "A"}, Evidence: rankEvidenceRaw()})
	if r["error"] == nil {
		t.Fatalf("INVARIANT 2: an unknown constraint must fail closed (error), never ALLOW; got %v", r)
	}
	if r["verdict"] == "ALLOW" || r["allowed"] == true {
		t.Fatalf("INVARIANT 2 violated: unknown constraint returned ALLOW: %v", r)
	}
}

func TestSessionTypoEvidenceFieldRejected(t *testing.T) {
	s := expSession()
	// `requestedRank` (camelCase typo) instead of `requested_rank` must NOT silently become 0.
	bad := json.RawMessage(`{"requestedRank":2,"evidence_complete":true,"options":[{"id":"A","price":163}]}`)
	r := s.check(sessionCmd{Constraint: "rank", Scope: "txn-1",
		Proposed: supervise.ProposedAction{Option: "A"}, Evidence: bad})
	if r["error"] == nil {
		t.Fatalf("INVARIANT 8: a typo'd evidence field must fail loudly, not silently ALLOW; got %v", r)
	}
	if r["verdict"] == "ALLOW" {
		t.Fatalf("INVARIANT 8 violated: typo'd evidence produced ALLOW: %v", r)
	}
}

func TestSessionMissingRankRejected(t *testing.T) {
	s := expSession()
	noRank := json.RawMessage(`{"evidence_complete":true,"options":[{"id":"A","price":163}]}`)
	r := s.check(sessionCmd{Constraint: "rank", Scope: "txn-1",
		Proposed: supervise.ProposedAction{Option: "A"}, Evidence: noRank})
	if r["error"] == nil {
		t.Fatalf("a rank check with no requested_rank must be refused; got %v", r)
	}
}

func TestSessionEmptyEvidenceRejected(t *testing.T) {
	s := expSession()
	r := s.check(sessionCmd{Constraint: "rank", Scope: "txn-1",
		Proposed: supervise.ProposedAction{Option: "A"}, Evidence: json.RawMessage(`{}`)})
	if r["error"] == nil {
		t.Fatalf("empty evidence must be refused (missing requested_rank); got %v", r)
	}
}

// ---- Phase 3: transaction/entity binding (INVARIANT 5) ----

func TestSessionScopeIsolatesRetryBudgets(t *testing.T) {
	s := expSession() // budget 3
	// Exhaust entity A's budget entirely.
	for i := 0; i < 5; i++ {
		s.check(sessionCmd{Constraint: "rank", Scope: "order-A",
			Proposed: supervise.ProposedAction{Option: "A"}, Evidence: rankEvidenceRaw()})
	}
	if got, _ := s.store.RetryCount(retryKey("order-A", "rank")); got == 0 {
		t.Fatal("order-A should have consumed its budget")
	}
	// Entity B, same constraint, must start FRESH — its first REJECT is not RECOVERY_EXHAUSTED.
	rb := s.check(sessionCmd{Constraint: "rank", Scope: "order-B",
		Proposed: supervise.ProposedAction{Option: "A"}, Evidence: rankEvidenceRaw()})
	if rb["verdict"] != "REJECT" {
		t.Fatalf("INVARIANT 5 violated: order-B inherited order-A's exhausted budget; got %v", rb["verdict"])
	}
	if n, _ := s.store.RetryCount(retryKey("order-B", "rank")); n != 1 {
		t.Fatalf("order-B retry state must be independent (1), got %d", n)
	}
}

// ---- Phase 5: replay / idempotency / no-execute-on-non-ALLOW ----

func allowOnce(t *testing.T, s *extSession, scope string) string {
	t.Helper()
	r := s.check(sessionCmd{Action: "tool_call", Tool: "book", Constraint: "rank", Scope: scope,
		Proposed: supervise.ProposedAction{Option: "B"}, Evidence: rankEvidenceRaw()})
	if r["verdict"] != "ALLOW" {
		t.Fatalf("expected ALLOW for rank-2 B, got %v", r)
	}
	return r["decision_id"].(string)
}

func TestSessionRefusesExecuteOnNonAllow(t *testing.T) {
	s := expSession()
	r := s.check(sessionCmd{Action: "tool_call", Tool: "book", Constraint: "rank", Scope: "txn-1",
		Proposed: supervise.ProposedAction{Option: "A"}, Evidence: rankEvidenceRaw()}) // rank-1 -> REJECT
	id := r["decision_id"].(string)
	exec := true
	rr := s.record(sessionCmd{Action: "tool_call", Tool: "book", Of: id, Executed: &exec})
	if rr["error"] == nil {
		t.Fatalf("INVARIANT 1: recording execution of a REJECTed decision must be refused; got %v", rr)
	}
}

func TestSessionExecutionIsIdempotent(t *testing.T) {
	s := expSession()
	id := allowOnce(t, s, "txn-1")
	exec := true
	act := &supervise.ProposedAction{Option: "B"}
	if rr := s.record(sessionCmd{Tool: "book", Of: id, Executed: &exec, ExecutedAction: act}); rr["error"] != nil {
		t.Fatalf("first execution record should succeed; got %v", rr)
	}
	rr2 := s.record(sessionCmd{Tool: "book", Of: id, Executed: &exec, ExecutedAction: act})
	if rr2["error"] == nil {
		t.Fatalf("a second execution confirmation on the same decision must be refused (idempotency); got %v", rr2)
	}
}

// D2: the action binding is MANDATORY. Omitting the executed action -> refused (fail closed).
func TestSessionExecutionRequiresExecutedAction(t *testing.T) {
	s := expSession()
	id := allowOnce(t, s, "txn-1")
	exec := true
	rr := s.record(sessionCmd{Tool: "book", Of: id, Executed: &exec}) // no ExecutedAction
	if rr["error"] == nil {
		t.Fatalf("recording execution of an ALLOW without executed_action must be refused; got %v", rr)
	}
}

// D2: check A, execute B -> refused (different option).
func TestSessionRefusesModifiedAction_DifferentOption(t *testing.T) {
	s := expSession()
	id := allowOnce(t, s, "txn-1") // checked Option B
	exec := true
	rr := s.record(sessionCmd{Tool: "book", Of: id, Executed: &exec,
		ExecutedAction: &supervise.ProposedAction{Option: "A"}}) // executed a DIFFERENT option
	if rr["error"] == nil {
		t.Fatalf("INVARIANT 7: executing a different action than the checked one must be refused; got %v", rr)
	}
}

// D2: same tool, changed field (amount/customer) -> refused.
func TestSessionRefusesModifiedAction_ChangedField(t *testing.T) {
	s := expSession()
	r := s.check(sessionCmd{Action: "tool_call", Tool: "book", Constraint: "rank", Scope: "txn-1",
		Proposed: supervise.ProposedAction{Option: "B", Fields: map[string]any{"customer": "91"}},
		Evidence: rankEvidenceRaw()})
	id := r["decision_id"].(string)
	exec := true
	rr := s.record(sessionCmd{Tool: "book", Of: id, Executed: &exec,
		ExecutedAction: &supervise.ProposedAction{Option: "B", Fields: map[string]any{"customer": "92"}}})
	if rr["error"] == nil {
		t.Fatalf("changing a field after ALLOW must be refused; got %v", rr)
	}
}

func TestSessionAllowExecutesWithMatchingAction(t *testing.T) {
	s := expSession()
	r := s.check(sessionCmd{Action: "tool_call", Tool: "book", Constraint: "rank", Scope: "txn-1",
		Proposed: supervise.ProposedAction{Option: "B"}, Evidence: rankEvidenceRaw()})
	id := r["decision_id"].(string)
	exec := true
	rr := s.record(sessionCmd{Tool: "book", Of: id, Executed: &exec,
		ExecutedAction: &supervise.ProposedAction{Option: "B"}}) // exact checked action
	if rr["error"] != nil {
		t.Fatalf("executing the exact checked action must be allowed; got %v", rr)
	}
}

// D2: fingerprint is order-independent — Fields key order must not change the match.
func TestSessionActionBindingIsKeyOrderIndependent(t *testing.T) {
	s := expSession()
	r := s.check(sessionCmd{Action: "tool_call", Tool: "book", Constraint: "rank", Scope: "txn-1",
		Proposed: supervise.ProposedAction{Option: "B", Fields: map[string]any{"a": 1.0, "b": 2.0}},
		Evidence: rankEvidenceRaw()})
	id := r["decision_id"].(string)
	exec := true
	// same content, fields built in a different insertion order -> Go map -> canonical JSON sorts keys
	rr := s.record(sessionCmd{Tool: "book", Of: id, Executed: &exec,
		ExecutedAction: &supervise.ProposedAction{Option: "B", Fields: map[string]any{"b": 2.0, "a": 1.0}}})
	if rr["error"] != nil {
		t.Fatalf("key-order-only difference must still match; got %v", rr)
	}
}

// D3: verdict time and execution time are recorded distinctly, at their actual moments.
func TestSessionRecordsDistinctVerdictAndExecutionTimes(t *testing.T) {
	s := expSession()
	r := s.check(sessionCmd{Action: "tool_call", Tool: "book", Constraint: "rank", Scope: "txn-1",
		Proposed: supervise.ProposedAction{Option: "B"}, Evidence: rankEvidenceRaw()})
	id := r["decision_id"].(string)
	rec := s.byID[id]
	if rec.Timestamp.IsZero() {
		t.Fatal("D3: the verdict decision must be timestamped at check time, not at finish")
	}
	checkedAt := rec.Timestamp
	exec := true
	s.record(sessionCmd{Tool: "book", Of: id, Executed: &exec, ExecutedAction: &supervise.ProposedAction{Option: "B"}})
	if rec.Supervision == nil || rec.Supervision.ExecutedAtUnix == 0 {
		t.Fatal("D3: execution time must be recorded on the supervision block")
	}
	if rec.Supervision.ExecutedAtUnix < checkedAt.Unix() {
		t.Fatalf("execution time (%d) must be >= verdict time (%d)", rec.Supervision.ExecutedAtUnix, checkedAt.Unix())
	}
}

// D3: finish() must not overwrite the actual per-decision timestamps.
func TestSessionTimestampsSurviveFinish(t *testing.T) {
	s := expSession()
	r1 := s.check(sessionCmd{Action: "tool_call", Tool: "book", Constraint: "rank", Scope: "txn-1",
		Proposed: supervise.ProposedAction{Option: "A"}, Evidence: rankEvidenceRaw()}) // REJECT
	r2 := s.check(sessionCmd{Action: "tool_call", Tool: "book", Constraint: "rank", Scope: "txn-1",
		Proposed: supervise.ProposedAction{Option: "B"}, Evidence: rankEvidenceRaw()}) // ALLOW
	id1, id2 := r1["decision_id"].(string), r2["decision_id"].(string)
	ts1, ts2 := s.byID[id1].Timestamp, s.byID[id2].Timestamp
	if ts1.IsZero() || ts2.IsZero() {
		t.Fatal("both decisions must be stamped at check time")
	}
	if ts2.Before(ts1) {
		t.Fatal("the later check must not have an earlier timestamp")
	}
	exec := true
	s.record(sessionCmd{Tool: "book", Of: id2, Executed: &exec, ExecutedAction: &supervise.ProposedAction{Option: "B"}})
	run := runResultOf(t, s.finish(sessionCmd{Success: true, Output: "booked B"}))
	if !run.Decisions[0].Timestamp.Equal(ts1) || !run.Decisions[1].Timestamp.Equal(ts2) {
		t.Fatalf("finish() overwrote decision timestamps: got %v,%v want %v,%v",
			run.Decisions[0].Timestamp, run.Decisions[1].Timestamp, ts1, ts2)
	}
	if run.Decisions[0].Timestamp.Location().String() != "UTC" {
		t.Fatal("timestamps must be UTC")
	}
}

// ---- Phase 4: freshness at the session boundary ----

func TestSessionFreshnessStaleRequireEvidence(t *testing.T) {
	s := expSession()
	stale := json.RawMessage(`{"requested_rank":2,"evidence_complete":true,` +
		`"options":[{"id":"A","price":163,"is_direct":true},{"id":"B","price":290,"is_direct":true}],` +
		`"meta":{"observed_at_unix":1000000}}`)
	r := s.check(sessionCmd{Action: "tool_call", Tool: "book", Constraint: "rank", Scope: "txn-1",
		Proposed: supervise.ProposedAction{Option: "B"}, Evidence: stale, MaxEvidenceAgeSec: 60})
	if r["verdict"] != "REQUIRE_EVIDENCE" {
		t.Fatalf("stale evidence under a freshness policy must REQUIRE_EVIDENCE; got %v", r["verdict"])
	}
}
