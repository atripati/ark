package main

import (
	"os"
	"testing"

	"github.com/atripati/ark/pkg/authz"
	"github.com/atripati/ark/pkg/supervise"
)

// durableSession builds a session whose authorization state lives in a durable FileStore rooted
// at dir. A "restart" is modeled by a second durableSession on the SAME dir (a fresh in-memory
// process with no session state, but sharing the persisted store).
func durableSession(t *testing.T, dir string) *extSession {
	t.Helper()
	t.Setenv("ARK_AUTHZ_DIR", dir)
	s := newExtSession(sessionCmd{Task: "durable", Supervision: "experimental", Budget: 3, RunID: "run-1"})
	if s.storeErr != nil {
		t.Fatalf("durable store failed to open: %v", s.storeErr)
	}
	return s
}

func issueB(t *testing.T, s *extSession, txn string) string {
	t.Helper()
	r := s.check(sessionCmd{Action: "tool_call", Tool: "book", Constraint: "rank", Scope: "order-1",
		TransactionID: txn, Proposed: supervise.ProposedAction{Option: "B"}, Evidence: rankEvidenceRaw()})
	if r["verdict"] != "ALLOW" {
		t.Fatalf("expected ALLOW, got %v", r)
	}
	return r["authorization_id"].(string)
}

// CASE A / ATTACK 1: check -> ISSUED; restart; consume still resolves against the durable store.
func TestDurable_IssueSurvivesRestartAndConsumes(t *testing.T) {
	dir := t.TempDir()
	authID := issueB(t, durableSession(t, dir), "txn-1")
	s2 := durableSession(t, dir) // restart
	cr := s2.consume(sessionCmd{AuthorizationID: authID, ExecutedAction: &supervise.ProposedAction{Option: "B"}})
	if cr["cleared"] != true {
		t.Fatalf("a persisted ISSUED authorization must consume after restart; got %v", cr)
	}
}

// CASE B / ATTACK 2: consume -> CONSUMED; crash/restart; consume again REFUSED.
func TestDurable_ConsumedStaysConsumedAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	s1 := durableSession(t, dir)
	authID := issueB(t, s1, "txn-1")
	act := &supervise.ProposedAction{Option: "B"}
	if cr := s1.consume(sessionCmd{AuthorizationID: authID, ExecutedAction: act}); cr["cleared"] != true {
		t.Fatalf("first consume must clear; got %v", cr)
	}
	s2 := durableSession(t, dir) // restart AFTER consume, BEFORE the tool outcome is known
	cr2 := s2.consume(sessionCmd{AuthorizationID: authID, ExecutedAction: act})
	if cr2["error"] == nil {
		t.Fatalf("a CONSUMED authorization must stay consumed across restart (ambiguous outcome, not re-issued); got %v", cr2)
	}
}

// ATTACK 5: retry exhaustion must NOT reset after restart (no fresh budget).
func TestDurable_RetryExhaustionSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	s1 := durableSession(t, dir) // budget 3
	for i := 0; i < 3; i++ {
		r := s1.check(sessionCmd{Constraint: "rank", Scope: "order-1", TransactionID: "txn-1",
			Proposed: supervise.ProposedAction{Option: "A"}, Evidence: rankEvidenceRaw()}) // rank-1 -> REJECT
		if r["verdict"] != "REJECT" {
			t.Fatalf("attempt %d: want REJECT, got %v", i, r["verdict"])
		}
	}
	s2 := durableSession(t, dir) // restart
	r := s2.check(sessionCmd{Constraint: "rank", Scope: "order-1", TransactionID: "txn-1",
		Proposed: supervise.ProposedAction{Option: "A"}, Evidence: rankEvidenceRaw()})
	if r["verdict"] != "RECOVERY_EXHAUSTED" {
		t.Fatalf("restart must NOT reset an exhausted budget into a fresh one; got %v", r["verdict"])
	}
}

// ATTACK 9: a different transaction cannot consume an authorization it does not own.
func TestDurable_WrongTransactionRefused(t *testing.T) {
	dir := t.TempDir()
	authID := issueB(t, durableSession(t, dir), "txn-A")
	s2 := durableSession(t, dir)
	cr := s2.consume(sessionCmd{AuthorizationID: authID, TransactionID: "txn-B",
		ExecutedAction: &supervise.ProposedAction{Option: "B"}})
	if cr["error"] == nil {
		t.Fatalf("consume from a different transaction must be refused (DUR-07); got %v", cr)
	}
}

// ATTACK 6: a store write failure during consume must NEVER clear execution.
func TestDurable_StoreFailureDuringConsumeFailsClosed(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root bypasses directory permissions")
	}
	dir := t.TempDir()
	s := durableSession(t, dir)
	authID := issueB(t, s, "txn-1")
	if err := os.Chmod(dir, 0o500); err != nil { // read+exec, no write -> marker create fails
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o755)
	cr := s.consume(sessionCmd{AuthorizationID: authID, ExecutedAction: &supervise.ProposedAction{Option: "B"}})
	if cr["cleared"] == true || cr["error"] == nil {
		t.Fatalf("a store write failure during consume must fail closed (no clearance); got %v", cr)
	}
}

// ATTACK 12 / Phase 13: a crash after CONSUME but before COMPLETE leaves an explicit, ambiguous
// state a restarted process can observe and reconcile — never silently retried or fabricated.
func TestDurable_ConsumedCrashLeavesReconcilableState(t *testing.T) {
	dir := t.TempDir()
	s1 := durableSession(t, dir)
	authID := issueB(t, s1, "txn-1")
	act := &supervise.ProposedAction{Option: "B"}
	s1.consume(sessionCmd{AuthorizationID: authID, ExecutedAction: act}) // CONSUMED, then "crash"
	// restart: the operator asks for status to reconcile
	s2 := durableSession(t, dir)
	st := s2.status(sessionCmd{AuthorizationID: authID})
	if st["state"] != "CONSUMED" {
		t.Fatalf("a crashed-after-consume authorization must report CONSUMED; got %v", st)
	}
	if st["reconcile"] == "" {
		t.Fatalf("CONSUMED must surface an explicit AMBIGUOUS/reconcile note; got %v", st)
	}
	// a completed authorization reports COMPLETED with no ambiguity
	s3 := durableSession(t, dir)
	// consume was already done in s1; complete it now via record
	exec := true
	s3.record(sessionCmd{Tool: "book", AuthorizationID: authID, Executed: &exec, ExecutedAction: act})
	if st2 := s3.status(sessionCmd{AuthorizationID: authID}); st2["state"] != "COMPLETED" || st2["reconcile"] != "" {
		t.Fatalf("a completed authorization must report COMPLETED with no reconcile note; got %v", st2)
	}
}

// A status query for a valid-but-unknown authorization reports UNKNOWN (never fabricates a state).
func TestDurable_StatusUnknown(t *testing.T) {
	s := durableSession(t, t.TempDir())
	unknown := authz.ID("", "nonexistent", "", "rank", "x", "y") // valid id, never stored
	st := s.status(sessionCmd{AuthorizationID: unknown})
	if st["state"] != "UNKNOWN" {
		t.Fatalf("unknown authorization status must be UNKNOWN; got %v", st)
	}
	// a MALFORMED id is rejected (fail closed), never reported as a fabricated state
	if bad := s.status(sessionCmd{AuthorizationID: "ark-authz-doesnotexist"}); bad["error"] == nil {
		t.Fatalf("a malformed authorization id must be refused; got %v", bad)
	}
}

// ATTACK 7 (check-time store failure): supervision refuses when the durable store is unavailable.
func TestDurable_StoreUnavailableAtStartFailsClosed(t *testing.T) {
	// point ARK_AUTHZ_DIR under a regular file so OpenFileStore fails; the session records the
	// error and every check fails closed.
	base := t.TempDir()
	blocker := base + "/blocker"
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARK_AUTHZ_DIR", blocker+"/store")
	s := newExtSession(sessionCmd{Task: "d", Supervision: "experimental", Budget: 3})
	r := s.check(sessionCmd{Action: "tool_call", Tool: "book", Constraint: "rank", Scope: "order-1",
		TransactionID: "txn-1", Proposed: supervise.ProposedAction{Option: "B"}, Evidence: rankEvidenceRaw()})
	if r["error"] == nil {
		t.Fatalf("check with an unavailable durable store must fail closed; got %v", r)
	}
}
