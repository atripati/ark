package main

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/atripati/ark/pkg/supervise"
	"github.com/atripati/ark/pkg/telemetry"
)

func runResultOf(t *testing.T, reply map[string]any) telemetry.RunResult {
	t.Helper()
	if reply["error"] != nil {
		t.Fatalf("finish returned error: %v", reply["error"])
	}
	run, ok := reply["run_result"].(telemetry.RunResult)
	if !ok {
		t.Fatalf("finish reply has no telemetry.RunResult: %T", reply["run_result"])
	}
	return run
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// rankEvidenceRaw is the trusted evidence as the transport carries it: a raw JSON payload,
// so check() exercises the STRICT decode path (as a real Python caller would drive it).
func rankEvidenceRaw() json.RawMessage {
	b, _ := json.Marshal(supervise.Evidence{
		RequestedRank: 2, EvidenceComplete: true,
		Options: []supervise.Option{
			{ID: "A", Price: 163, IsDirect: true},
			{ID: "B", Price: 290, IsDirect: true},
		},
	})
	return b
}

// The whole point of the session process: retry state + verdict semantics are authoritative
// in Go. Python never counts retries; the session does.
func TestSessionRetryAuthoritativeInGo(t *testing.T) {
	s := newExtSession(sessionCmd{Task: "book", Supervision: "experimental", Budget: 3})
	want := []string{"REJECT", "REJECT", "REJECT", "RECOVERY_EXHAUSTED"}
	for i, w := range want {
		r := s.check(sessionCmd{Action: "tool_call", Tool: "book", Constraint: "rank", Scope: "txn-1",
			Proposed: supervise.ProposedAction{Option: "A"}, Evidence: rankEvidenceRaw()})
		if got := r["verdict"]; got != w {
			t.Fatalf("check %d: verdict=%v, want %v", i, got, w)
		}
	}
	if n, _ := s.store.RetryCount(retryKey("txn-1", "rank")); n != 4 {
		t.Fatalf("retry counter=%d, want 4", n)
	}
}

func TestSessionCheckOffByDefault(t *testing.T) {
	s := newExtSession(sessionCmd{Task: "x"}) // supervision defaults off
	r := s.check(sessionCmd{Constraint: "rank"})
	if r["error"] == nil {
		t.Fatalf("expected error when supervision is off, got %v", r)
	}
}

// The canonical reject -> re-propose -> allow -> execute chain, assembled into one RunResult.
func TestSessionFullChainRejectThenAllow(t *testing.T) {
	s := newExtSession(sessionCmd{Task: "book 2nd cheapest", Supervision: "experimental", Provider: "openai"})

	r1 := s.check(sessionCmd{Action: "tool_call", Tool: "book", Constraint: "rank", Scope: "txn-1",
		Proposed: supervise.ProposedAction{Option: "A"}, Evidence: rankEvidenceRaw()})
	if r1["verdict"] != "REJECT" || r1["suggested"] != "B" {
		t.Fatalf("first check: %v", r1)
	}
	r2 := s.check(sessionCmd{Action: "tool_call", Tool: "book", Constraint: "rank", Scope: "txn-1",
		Proposed: supervise.ProposedAction{Option: "B"}, Evidence: rankEvidenceRaw()})
	if r2["verdict"] != "ALLOW" || r2["allowed"] != true {
		t.Fatalf("second check: %v", r2)
	}
	allowID, _ := r2["decision_id"].(string)
	in, out := 882, 186
	rr := s.record(sessionCmd{Action: "tool_call", Tool: "book", Model: "gpt-4o",
		InputTokens: in, OutputTokens: out, Of: allowID,
		ExecutedAction: &supervise.ProposedAction{Option: "B"}}) // MANDATORY: the executed action
	if rr["error"] != nil {
		t.Fatalf("recording the authorized action must succeed: %v", rr)
	}

	fr := s.finish(sessionCmd{Success: true, Output: "booked B"})
	run := runResultOf(t, fr)

	if len(run.Decisions) != 2 {
		t.Fatalf("want 2 decisions, got %d", len(run.Decisions))
	}
	rej, allw := run.Decisions[0], run.Decisions[1]
	if rej.ID != "decision_001" || rej.Supervision == nil || rej.Supervision.Verdict != "REJECT" {
		t.Fatalf("rejected decision wrong: %+v", rej)
	}
	if rej.Executed || rej.Cost.TotalCost != 0 {
		t.Fatalf("rejected proposal must not be executed and must cost 0: %+v", rej)
	}
	if allw.ID != "decision_002" || allw.Supervision == nil || allw.Supervision.Verdict != "ALLOW" {
		t.Fatalf("allowed decision wrong: %+v", allw)
	}
	if !allw.Executed || allw.Model != "gpt-4o" {
		t.Fatalf("allowed+executed telemetry missing: %+v", allw)
	}
	// cost derived from tokens+model via pkg/cost pricing (gpt-4o: 2.5/M in, 10/M out)
	wantCost := float64(in)*2.5/1e6 + float64(out)*10.0/1e6
	if math.Abs(allw.Cost.TotalCost-wantCost) > 1e-9 {
		t.Fatalf("derived cost=%v want %v", allw.Cost.TotalCost, wantCost)
	}
	if !run.Supervision.Enabled || run.Supervision.Interventions != 1 {
		t.Fatalf("supervision summary wrong: %+v", run.Supervision)
	}
	if run.Providers["openai"] != "reported" {
		t.Fatalf("external provider status must be 'reported', got %v", run.Providers)
	}
}

func TestSessionDerivesCostOnlyWhenNotSupplied(t *testing.T) {
	s := newExtSession(sessionCmd{Task: "x", Provider: "openai"})
	supplied := 0.5
	s.record(sessionCmd{Action: "complete", Model: "mystery", InputTokens: 100, OutputTokens: 50, Cost: &supplied})
	fr := s.finish(sessionCmd{Success: true})
	run := runResultOf(t, fr)
	if math.Abs(run.Decisions[0].Cost.TotalCost-0.5) > 1e-9 {
		t.Fatalf("supplied cost overwritten: %v", run.Decisions[0].Cost.TotalCost)
	}
}

func TestSessionProvenanceMarksReportedVsDerived(t *testing.T) {
	s := newExtSession(sessionCmd{Task: "x", Provider: "openai"})
	s.record(sessionCmd{Action: "complete", Model: "gpt-4o", InputTokens: 100, OutputTokens: 50})
	prov := s.prov["decision_001"]
	if !contains(prov.Reported, "model") || !contains(prov.Reported, "input_tokens") {
		t.Fatalf("reported provenance missing model/tokens: %+v", prov.Reported)
	}
	if !contains(prov.Derived, "cost") {
		t.Fatalf("cost should be derived: %+v", prov.Derived)
	}
	if contains(prov.Reported, "cost") {
		t.Fatalf("derived cost must not be listed as reported: %+v", prov.Reported)
	}
	// dedup: a field touched by check+record appears once
	got := dedup([]string{"action", "action", "model"})
	if len(got) != 2 {
		t.Fatalf("dedup failed: %v", got)
	}
}
