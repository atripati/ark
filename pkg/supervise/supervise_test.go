package supervise

import (
	"errors"
	"testing"
)

// generic priced option set: A cheapest (direct), B second-cheapest (connecting), C third.
func fixture() Evidence {
	return Evidence{
		Options: []Option{
			{ID: "A", Price: 163, IsDirect: true},
			{ID: "B", Price: 290, IsDirect: false},
			{ID: "C", Price: 300, IsDirect: false},
		},
		RequestedRank:    2,
		NonstopOnly:      false,
		EvidenceComplete: true,
	}
}

// eval runs the rank constraint and fails the test on an unexpected error (the constraint is
// registered, so a well-formed request must not error).
func eval(t *testing.T, p ProposedAction, e Evidence, retry, budget int) Decision {
	t.Helper()
	d, err := New().Evaluate(Request{Constraint: "rank", Scope: "txn-1", Proposed: p, Evidence: e, RetryCount: retry, Budget: budget})
	if err != nil {
		t.Fatalf("unexpected error from Evaluate: %v", err)
	}
	return d
}

// ---- INVARIANT: no non-ALLOW verdict is ever marked executed (INV 1) ----

func assertNonAllowNotExecuted(t *testing.T, d Decision) {
	t.Helper()
	if d.Verdict != Allow && d.Audit.Executed {
		t.Fatalf("INVARIANT 1 violated: non-ALLOW verdict %s marked executed=true", d.Verdict)
	}
}

// ================= fail-closed configuration =================

func TestUnknownConstraint_FailsClosed(t *testing.T) {
	d, err := New().Evaluate(Request{Constraint: "refund_limit", Scope: "txn-1",
		Proposed: ProposedAction{Option: "A"}, Evidence: fixture(), Budget: 4})
	if err == nil {
		t.Fatal("INVARIANT 2 violated: unknown constraint must return an error")
	}
	if !errors.Is(err, ErrUnknownConstraint) {
		t.Fatalf("expected ErrUnknownConstraint, got %v", err)
	}
	if d.Verdict == Allow {
		t.Fatal("INVARIANT 2 violated: unknown constraint must never return ALLOW")
	}
	if d.Audit.Executed {
		t.Fatal("INVARIANT 2 violated: unknown constraint must never mark executed=true")
	}
}

func TestNoRankProvided_RequireEvidence(t *testing.T) {
	e := fixture()
	e.RequestedRank = 0 // missing rank is NOT "no preference" — it fails closed
	d := eval(t, ProposedAction{Option: "A"}, e, 0, 4)
	if d.Verdict != RequireEvidence {
		t.Fatalf("missing requested_rank must REQUIRE_EVIDENCE, not ALLOW; got %s", d.Verdict)
	}
	assertNonAllowNotExecuted(t, d)
}

// ================= insufficient / unverifiable evidence -> REQUIRE_EVIDENCE =================

func TestIncompleteEvidence_RequireEvidence(t *testing.T) {
	e := fixture()
	e.EvidenceComplete = false
	d := eval(t, ProposedAction{Option: "A"}, e, 0, 4)
	if d.Verdict != RequireEvidence {
		t.Fatalf("incomplete evidence must REQUIRE_EVIDENCE; got %s", d.Verdict)
	}
	assertNonAllowNotExecuted(t, d)
}

func TestNonstopOnlyIncompleteEvidence_RequireEvidence(t *testing.T) {
	e := fixture()
	e.NonstopOnly = true
	e.EvidenceComplete = false // the nonstop path used to skip the completeness check — now it fails closed
	d := eval(t, ProposedAction{Option: "A"}, e, 0, 4)
	if d.Verdict != RequireEvidence {
		t.Fatalf("nonstop-only + incomplete evidence must REQUIRE_EVIDENCE; got %s", d.Verdict)
	}
	assertNonAllowNotExecuted(t, d)
}

func TestNoRankableCandidates_RequireEvidence(t *testing.T) {
	e := Evidence{Options: []Option{}, RequestedRank: 2, EvidenceComplete: true}
	d := eval(t, ProposedAction{Option: "A"}, e, 0, 4)
	if d.Verdict != RequireEvidence {
		t.Fatalf("zero candidates must REQUIRE_EVIDENCE (never ALLOW); got %s", d.Verdict)
	}
	assertNonAllowNotExecuted(t, d)
}

func TestNonstopOnlyZeroDirects_RequireEvidence(t *testing.T) {
	// all connecting; nonstop-only filters everything out -> cannot verify -> REQUIRE_EVIDENCE.
	e := Evidence{Options: []Option{
		{ID: "X", Price: 100, IsDirect: false},
		{ID: "Y", Price: 200, IsDirect: false},
	}, RequestedRank: 1, NonstopOnly: true, EvidenceComplete: true}
	d := eval(t, ProposedAction{Option: "X"}, e, 0, 4)
	if d.Verdict != RequireEvidence {
		t.Fatalf("nonstop-only with zero direct candidates must REQUIRE_EVIDENCE; got %s", d.Verdict)
	}
	assertNonAllowNotExecuted(t, d)
}

func TestRankExceedsTiers_RequireEvidence(t *testing.T) {
	e := fixture()
	e.RequestedRank = 9 // only 3 tiers exist
	d := eval(t, ProposedAction{Option: "A"}, e, 0, 4)
	if d.Verdict != RequireEvidence {
		t.Fatalf("requested rank exceeding tiers must REQUIRE_EVIDENCE (never ALLOW); got %s", d.Verdict)
	}
	assertNonAllowNotExecuted(t, d)
}

func TestProposedOptionAbsent_RequireEvidence(t *testing.T) {
	d := eval(t, ProposedAction{Option: "ZZZ"}, fixture(), 0, 4) // not in evidence
	if d.Verdict != RequireEvidence {
		t.Fatalf("a proposed option absent from evidence must REQUIRE_EVIDENCE (never ALLOW); got %s", d.Verdict)
	}
	assertNonAllowNotExecuted(t, d)
}

func TestProposedOptionUnpriceable_RequireEvidence(t *testing.T) {
	// The proposed option exists but its price is not among the candidate tiers (nonstop-only
	// excludes it from the tier set, yet it is still the proposed id).
	e := Evidence{Options: []Option{
		{ID: "A", Price: 163, IsDirect: true},
		{ID: "D", Price: 200, IsDirect: true},
		{ID: "E", Price: 120, IsDirect: false}, // cheaper connecting; excluded by nonstop-only
	}, RequestedRank: 1, NonstopOnly: true, EvidenceComplete: true}
	d := eval(t, ProposedAction{Option: "E"}, e, 0, 4)
	if d.Verdict != RequireEvidence {
		t.Fatalf("a proposed option not priceable within the candidate tiers must REQUIRE_EVIDENCE; got %s", d.Verdict)
	}
	assertNonAllowNotExecuted(t, d)
}

// ================= provable outcomes =================

func TestRank1Proposal_RejectWithEvidence(t *testing.T) {
	d := eval(t, ProposedAction{Option: "A"}, fixture(), 0, 4) // A is rank-1, N=2
	if d.Verdict != Reject {
		t.Fatalf("rank-1 proposal under rank-2 request must REJECT; got %s", d.Verdict)
	}
	if d.Audit.SuggestedFromEvidence != "B" {
		t.Fatalf("rejection must point at the runtime rank-2 option B; got %q", d.Audit.SuggestedFromEvidence)
	}
	assertNonAllowNotExecuted(t, d)
}

func TestRank2Proposal_Allow(t *testing.T) {
	d := eval(t, ProposedAction{Option: "B"}, fixture(), 0, 4) // B is rank-2
	if d.Verdict != Allow || !d.Audit.Executed {
		t.Fatalf("rank-2 proposal must ALLOW+execute; got %s executed=%v", d.Verdict, d.Audit.Executed)
	}
}

func TestNonstopOnly_RestrictsToDirect(t *testing.T) {
	// two directs (A 163, D 200) + one cheaper connecting (E 120). Under nonstop-only, rank-2
	// must be the 2nd-cheapest DIRECT (D 200), never the cheaper connecting E.
	e := Evidence{
		Options: []Option{
			{ID: "A", Price: 163, IsDirect: true},
			{ID: "D", Price: 200, IsDirect: true},
			{ID: "E", Price: 120, IsDirect: false},
		},
		RequestedRank: 2, NonstopOnly: true, EvidenceComplete: true,
	}
	d := eval(t, ProposedAction{Option: "A"}, e, 0, 4) // A is rank-1 among directs
	if d.Verdict != Reject || d.Audit.SuggestedFromEvidence != "D" {
		t.Fatalf("nonstop-only rank-2 must target the 2nd-cheapest DIRECT D; got %s target=%q",
			d.Verdict, d.Audit.SuggestedFromEvidence)
	}
	// without nonstop-only, global order E(120) rank1, A(163) rank2, D(200) rank3 -> proposing A is rank-2 -> ALLOW
	e2 := e
	e2.NonstopOnly = false
	d2 := eval(t, ProposedAction{Option: "A"}, e2, 0, 4)
	if d2.Verdict != Allow {
		t.Fatalf("without nonstop-only, A is global rank-2 -> ALLOW; got %s", d2.Verdict)
	}
}

func TestBudgetExhaustion_RecoveryExhausted(t *testing.T) {
	p := ProposedAction{Option: "A"} // always rank-1 -> would REJECT
	if d := eval(t, p, fixture(), 3, 4); d.Verdict != Reject {
		t.Fatalf("retry 3 < budget 4 must still REJECT; got %s", d.Verdict)
	}
	d := eval(t, p, fixture(), 4, 4)
	if d.Verdict != RecoveryExhausted {
		t.Fatalf("retry 4 >= budget 4 must be RECOVERY_EXHAUSTED; got %s", d.Verdict)
	}
	if d.Audit.Executed {
		t.Fatal("INVARIANT 4 violated: RECOVERY_EXHAUSTED must never execute a known-violating action")
	}
}

func TestRecoveryExhausted_AlsoForRequireEvidence(t *testing.T) {
	// A persistently under-evidenced proposal must also exhaust to RECOVERY_EXHAUSTED (never ALLOW).
	e := fixture()
	e.EvidenceComplete = false
	d := eval(t, ProposedAction{Option: "A"}, e, 4, 4)
	if d.Verdict != RecoveryExhausted || d.Audit.Executed {
		t.Fatalf("persistent REQUIRE_EVIDENCE past budget must be RECOVERY_EXHAUSTED and not execute; got %s executed=%v", d.Verdict, d.Audit.Executed)
	}
}

// ================= freshness =================

func TestFreshnessStale_RequireEvidence(t *testing.T) {
	e := fixture()
	e.Meta.ObservedAtUnix = 1_000_000
	d, err := New().Evaluate(Request{Constraint: "rank", Scope: "txn-1",
		Proposed: ProposedAction{Option: "B"}, Evidence: e, Budget: 4,
		NowUnix: 1_000_000 + 3600, Freshness: FreshnessPolicy{MaxAgeSec: 60}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Verdict != RequireEvidence {
		t.Fatalf("stale evidence under a freshness policy must REQUIRE_EVIDENCE; got %s", d.Verdict)
	}
	assertNonAllowNotExecuted(t, d)
}

func TestFreshnessMissingTimestamp_RequireEvidence(t *testing.T) {
	e := fixture() // no observed_at
	d, _ := New().Evaluate(Request{Constraint: "rank", Scope: "txn-1",
		Proposed: ProposedAction{Option: "B"}, Evidence: e, Budget: 4,
		NowUnix: 2_000_000, Freshness: FreshnessPolicy{MaxAgeSec: 60}})
	if d.Verdict != RequireEvidence {
		t.Fatalf("missing observed_at under a freshness policy must REQUIRE_EVIDENCE; got %s", d.Verdict)
	}
}

func TestFreshnessFresh_EvaluatesNormally(t *testing.T) {
	e := fixture()
	e.Meta.ObservedAtUnix = 2_000_000
	d, err := New().Evaluate(Request{Constraint: "rank", Scope: "txn-1",
		Proposed: ProposedAction{Option: "B"}, Evidence: e, Budget: 4,
		NowUnix: 2_000_000 + 10, Freshness: FreshnessPolicy{MaxAgeSec: 60}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Verdict != Allow { // B is rank-2; fresh evidence -> normal evaluation
		t.Fatalf("fresh evidence must evaluate normally (ALLOW for rank-2 B); got %s", d.Verdict)
	}
}

func TestFreshnessFutureTimestamp_RequireEvidence(t *testing.T) {
	e := fixture()
	e.Meta.ObservedAtUnix = 9_000_000_000 // ~year 2255
	d, _ := New().Evaluate(Request{Constraint: "rank", Scope: "txn-1",
		Proposed: ProposedAction{Option: "B"}, Evidence: e, Budget: 4,
		NowUnix: 2_000_000_000, Freshness: FreshnessPolicy{MaxAgeSec: 60, SkewSec: 60}})
	if d.Verdict != RequireEvidence {
		t.Fatalf("a far-future observed_at must never look fresh; want REQUIRE_EVIDENCE, got %s", d.Verdict)
	}
	assertNonAllowNotExecuted(t, d)
}

func TestCheckFreshness_SkewBoundary(t *testing.T) {
	now := int64(1_000_000)
	// within skew -> fresh
	if ok, _ := CheckFreshness(now+30, 0, now, 3600, 60); !ok {
		t.Fatal("observed 30s ahead within 60s skew must be fresh")
	}
	// beyond skew -> not fresh
	if ok, _ := CheckFreshness(now+120, 0, now, 3600, 60); ok {
		t.Fatal("observed 120s ahead beyond 60s skew must not be fresh")
	}
	// missing observed_at -> not fresh
	if ok, _ := CheckFreshness(0, 0, now, 3600, 60); ok {
		t.Fatal("missing observed_at must not be fresh")
	}
	// expired -> not fresh
	if ok, _ := CheckFreshness(now-10, now-1, now, 0, 60); ok {
		t.Fatal("expires_at in the past must not be fresh")
	}
}

func TestFreshnessExpired_RequireEvidence(t *testing.T) {
	e := fixture()
	e.Meta.ObservedAtUnix = 2_000_000
	e.Meta.ExpiresAtUnix = 2_000_050
	d, _ := New().Evaluate(Request{Constraint: "rank", Scope: "txn-1",
		Proposed: ProposedAction{Option: "B"}, Evidence: e, Budget: 4,
		NowUnix: 2_000_100, Freshness: FreshnessPolicy{MaxAgeSec: 3600}})
	if d.Verdict != RequireEvidence {
		t.Fatalf("expired evidence must REQUIRE_EVIDENCE; got %s", d.Verdict)
	}
}

// ================= audit / fingerprints =================

func TestAuditFields_Populated(t *testing.T) {
	d := eval(t, ProposedAction{Option: "A"}, fixture(), 1, 4)
	a := d.Audit
	if a.Constraint != "rank" || !a.Applicable || a.Proposed.Option != "A" ||
		a.Verdict != Reject || a.RejectionReason == "" || a.RetryNumber != 1 ||
		len(a.EvidenceUsed.Options) != 3 {
		t.Fatalf("audit record missing required fields: %+v", a)
	}
	if a.Scope != "txn-1" {
		t.Fatalf("audit must record the scope; got %q", a.Scope)
	}
	if a.ProposedFingerprint == "" || a.EvidenceFingerprint == "" {
		t.Fatalf("audit must record proposed+evidence fingerprints; got prop=%q ev=%q", a.ProposedFingerprint, a.EvidenceFingerprint)
	}
}

func TestFingerprintsBindActionAndEvidence(t *testing.T) {
	dA := eval(t, ProposedAction{Option: "A"}, fixture(), 0, 4)
	dB := eval(t, ProposedAction{Option: "B"}, fixture(), 0, 4)
	if dA.Audit.ProposedFingerprint == dB.Audit.ProposedFingerprint {
		t.Fatal("different proposed actions must have different fingerprints")
	}
	e2 := fixture()
	e2.Options[0].Price = 999
	dE := eval(t, ProposedAction{Option: "A"}, e2, 0, 4)
	if dA.Audit.EvidenceFingerprint == dE.Audit.EvidenceFingerprint {
		t.Fatal("different evidence states must have different fingerprints")
	}
}
