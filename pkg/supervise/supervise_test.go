package supervise

import "testing"

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

func eval(t *testing.T, p ProposedAction, e Evidence, retry, budget int) Decision {
	t.Helper()
	return New().Evaluate(Request{Constraint: "rank", Proposed: p, Evidence: e, RetryCount: retry, Budget: budget})
}

func TestNotApplicable_NoRank(t *testing.T) {
	e := fixture()
	e.RequestedRank = 0
	d := eval(t, ProposedAction{Option: "A"}, e, 0, 4)
	if d.Verdict != Allow || d.Audit.Applicable {
		t.Fatalf("no rank preference must be not-applicable ALLOW; got %s applicable=%v", d.Verdict, d.Audit.Applicable)
	}
}

func TestIncompleteEvidence_RequireEvidence(t *testing.T) {
	e := fixture()
	e.EvidenceComplete = false
	d := eval(t, ProposedAction{Option: "A"}, e, 0, 4)
	if d.Verdict != RequireEvidence {
		t.Fatalf("incomplete evidence must REQUIRE_EVIDENCE; got %s", d.Verdict)
	}
}

func TestRank1Proposal_RejectWithEvidence(t *testing.T) {
	d := eval(t, ProposedAction{Option: "A"}, fixture(), 0, 4) // A is rank-1, N=2
	if d.Verdict != Reject {
		t.Fatalf("rank-1 proposal under rank-2 request must REJECT; got %s", d.Verdict)
	}
	if d.Audit.SuggestedFromEvidence != "B" {
		t.Fatalf("rejection must point at the runtime rank-2 option B; got %q", d.Audit.SuggestedFromEvidence)
	}
	if d.Audit.Executed {
		t.Fatal("a rejected action must not be marked executed")
	}
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
	// without nonstop-only, global rank-2 is the connecting E is rank... A163,E... wait E120<A163
	e2 := e
	e2.NonstopOnly = false
	// global order: E(120) rank1, A(163) rank2, D(200) rank3 -> proposing A is rank-2 -> ALLOW
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
	if d := eval(t, p, fixture(), 4, 4); d.Verdict != RecoveryExhausted {
		t.Fatalf("retry 4 >= budget 4 must be RECOVERY_EXHAUSTED; got %s", d.Verdict)
	}
	if d := eval(t, p, fixture(), 4, 4); d.Audit.Executed {
		t.Fatal("RECOVERY_EXHAUSTED must never execute a known-violating action")
	}
}

func TestAuditFields_Populated(t *testing.T) {
	d := eval(t, ProposedAction{Option: "A"}, fixture(), 1, 4)
	a := d.Audit
	if a.Constraint != "rank" || !a.Applicable || a.Proposed.Option != "A" ||
		a.Verdict != Reject || a.RejectionReason == "" || a.RetryNumber != 1 ||
		len(a.EvidenceUsed.Options) != 3 {
		t.Fatalf("audit record missing required fields: %+v", a)
	}
}
