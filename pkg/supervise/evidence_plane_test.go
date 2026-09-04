package supervise

import "testing"

// trustedThreshold builds a well-formed TRUSTED_PROVIDER envelope for the threshold constraint,
// bound to (ns, txn, scope). Attacks mutate one field to prove each binding fails closed.
func trustedThreshold(ns, txn, scope string, limit float64) Evidence {
	return Evidence{
		Facts: map[string]float64{"limit": limit},
		Meta: EvidenceMeta{
			Trust: TrustProvider, ProviderID: "billing-system", Subject: scope, Namespace: ns,
			RequestFingerprint: RequestFingerprint(ns, txn, scope, "threshold"),
			ObservedAtUnix:     1000, Version: "v1", Source: "billing-db", EvidenceID: "snap-1",
		},
	}
}

func evalThreshold(t *testing.T, ns, txn, scope string, amount float64, e Evidence) Decision {
	t.Helper()
	d, err := New().Evaluate(Request{
		Constraint: "threshold", Namespace: ns, Transaction: txn, Scope: scope,
		Proposed: ProposedAction{Kind: "refund", Fields: map[string]any{"amount": amount}},
		Evidence: e, Budget: 4,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Verdict != Allow && d.Audit.Executed {
		t.Fatalf("non-ALLOW %s marked executed", d.Verdict)
	}
	return d
}

// ATTACK 1 (poisoned source): the agent proposes a $4000 refund; the trusted provider says the
// limit is $500. ARK uses the trusted fact and REJECTs — the agent cannot raise its own ceiling.
func TestEvidencePlane_PoisonedProposalRejectedByTrustedFact(t *testing.T) {
	d := evalThreshold(t, "", "txn-1", "customer-91", 4000, trustedThreshold("", "txn-1", "customer-91", 500))
	if d.Verdict != Reject {
		t.Fatalf("a proposal above the trusted limit must REJECT; got %s", d.Verdict)
	}
}

func TestEvidencePlane_WithinTrustedLimitAllows(t *testing.T) {
	d := evalThreshold(t, "", "txn-1", "customer-91", 100, trustedThreshold("", "txn-1", "customer-91", 500))
	if d.Verdict != Allow {
		t.Fatalf("a proposal within the trusted limit must ALLOW; got %s (%s)", d.Verdict, d.Reason)
	}
}

// ATTACK 2 (forged / caller-supplied): the same facts but NOT from a trusted provider cannot
// authorize a protected constraint.
func TestEvidencePlane_CallerSuppliedCannotSatisfyProtected(t *testing.T) {
	e := trustedThreshold("", "txn-1", "customer-91", 100000) // generous limit...
	e.Meta.Trust = TrustCallerSupplied                         // ...but not trusted
	d := evalThreshold(t, "", "txn-1", "customer-91", 4000, e)
	if d.Verdict != RequireEvidence {
		t.Fatalf("caller-supplied evidence must not satisfy a protected constraint; got %s", d.Verdict)
	}
}

func TestEvidencePlane_AgentSuppliedCannotSatisfyProtected(t *testing.T) {
	e := trustedThreshold("", "txn-1", "customer-91", 100000)
	e.Meta.Trust = TrustAgentSupplied
	if d := evalThreshold(t, "", "txn-1", "customer-91", 4000, e); d.Verdict != RequireEvidence {
		t.Fatalf("agent-supplied evidence must not satisfy a protected constraint; got %s", d.Verdict)
	}
	// no trust at all (empty) also fails closed
	e.Meta.Trust = ""
	if d := evalThreshold(t, "", "txn-1", "customer-91", 4000, e); d.Verdict != RequireEvidence {
		t.Fatalf("untrusted evidence must not satisfy a protected constraint; got %s", d.Verdict)
	}
}

// ATTACK 3 (wrong customer): evidence about customer-A cannot authorize an action on customer-B.
func TestEvidencePlane_WrongSubjectRefused(t *testing.T) {
	e := trustedThreshold("", "txn-1", "customer-A", 100000) // subject + fingerprint bound to A
	d := evalThreshold(t, "", "txn-1", "customer-B", 4000, e) // action scope is B
	if d.Verdict != RequireEvidence {
		t.Fatalf("evidence for a different subject must be refused; got %s", d.Verdict)
	}
}

// ATTACK 4 (wrong tenant): evidence in namespace X cannot authorize an action in namespace Y.
func TestEvidencePlane_WrongNamespaceRefused(t *testing.T) {
	e := trustedThreshold("tenant-X", "txn-1", "customer-91", 100000)
	d := evalThreshold(t, "tenant-Y", "txn-1", "customer-91", 4000, e)
	if d.Verdict != RequireEvidence {
		t.Fatalf("evidence from a different tenant must be refused; got %s", d.Verdict)
	}
}

// ATTACK 10 (replay): evidence resolved for transaction A cannot be reused for transaction B.
func TestEvidencePlane_ReplayAcrossTransactionRefused(t *testing.T) {
	e := trustedThreshold("", "txn-A", "customer-91", 100000) // fingerprint bound to txn-A
	d := evalThreshold(t, "", "txn-B", "customer-91", 4000, e)
	if d.Verdict != RequireEvidence {
		t.Fatalf("evidence resolved for another transaction must be refused; got %s", d.Verdict)
	}
}

// ATTACK 8 (incomplete required fields): a trusted envelope missing the 'limit' fact -> REQUIRE_EVIDENCE.
func TestEvidencePlane_MissingRequiredFactRequiresEvidence(t *testing.T) {
	e := trustedThreshold("", "txn-1", "customer-91", 500)
	delete(e.Facts, "limit")
	if d := evalThreshold(t, "", "txn-1", "customer-91", 100, e); d.Verdict != RequireEvidence {
		t.Fatalf("missing required fact must REQUIRE_EVIDENCE; got %s", d.Verdict)
	}
}

// A per-request RequireTrustedProvider flag can protect ANY constraint (here: rank), so caller-
// supplied rank evidence no longer authorizes.
func TestEvidencePlane_RequestFlagProtectsAnyConstraint(t *testing.T) {
	e := fixture() // caller-supplied rank evidence (no trust)
	d, err := New().Evaluate(Request{
		Constraint: "rank", Scope: "order-1", Transaction: "txn-1",
		Proposed: ProposedAction{Option: "B"}, Evidence: e, Budget: 4,
		RequireTrustedProvider: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Verdict != RequireEvidence {
		t.Fatalf("RequireTrustedProvider must block caller-supplied evidence even for rank; got %s", d.Verdict)
	}
}

// Threshold is a REGISTERED protected constraint (identity/genericity check).
func TestEvidencePlane_ThresholdRegisteredAndProtected(t *testing.T) {
	s := New()
	if !s.Has("threshold") {
		t.Fatal("threshold constraint must be registered")
	}
	if tr, ok := s.constraints["threshold"].(TrustedProviderRequirer); !ok || !tr.RequiresTrustedProvider() {
		t.Fatal("threshold must declare RequiresTrustedProvider() == true")
	}
}
