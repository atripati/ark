package supervise

import (
	"fmt"
	"testing"
)

// ---- Two REALISTIC third-party constraints, used to prove the applicability contract is
// generic (not rank-specific) and that a missing field can never become ALLOW. ----

// refundLimit governs refund actions and rejects refunds over a fixed cap. Its Applicable is
// written the idiomatic way a domain author would write it — and it must fail CLOSED (return
// CannotDetermine), never DoesNotApply, when it cannot see the fields it needs.
type refundLimit struct{}

func (refundLimit) Name() string { return "refund_limit" }
func (refundLimit) Applicable(p ProposedAction, _ Evidence) Applicability {
	if p.Kind == "" {
		return CannotDetermine // cannot tell what this action is -> fail closed
	}
	if p.Kind != "refund" {
		return DoesNotApply // positively a non-refund action
	}
	if _, ok := p.Fields["amount"]; !ok {
		return CannotDetermine // it is a refund, but the amount is not visible -> fail closed
	}
	return Applies
}
func (refundLimit) Validate(p ProposedAction, _ Evidence) (Verdict, string, string) {
	const limit = 500.0
	amt, _ := p.Fields["amount"].(float64) // Applicable guaranteed presence
	if amt > limit {
		return Reject, fmt.Sprintf("refund %.0f exceeds limit %.0f", amt, limit), ""
	}
	return Allow, fmt.Sprintf("refund %.0f within limit %.0f", amt, limit), ""
}

// purchaseThreshold governs purchase actions against a numeric cap it also validates in
// evidence. (For this test the cap is carried in evidence.RequestedRank — Evidence is a fixed
// struct here; the point is the applicability + own-validator contract, not a real schema.)
type purchaseThreshold struct{}

func (purchaseThreshold) Name() string { return "purchase_threshold" }
func (purchaseThreshold) Applicable(p ProposedAction, _ Evidence) Applicability {
	if p.Kind == "" {
		return CannotDetermine
	}
	if p.Kind != "purchase" {
		return DoesNotApply
	}
	return Applies
}
func (purchaseThreshold) Validate(p ProposedAction, e Evidence) (Verdict, string, string) {
	amt, ok := p.Fields["amount"].(float64)
	if !ok {
		return RequireEvidence, "purchase amount missing/unparseable", ""
	}
	cap := float64(e.RequestedRank)
	if amt > cap {
		return Reject, fmt.Sprintf("purchase %.0f exceeds threshold %.0f", amt, cap), ""
	}
	return Allow, "within threshold", ""
}
func (purchaseThreshold) ValidateEvidence(e Evidence) error {
	if e.RequestedRank <= 0 {
		return fmt.Errorf("purchase_threshold requires a positive threshold")
	}
	return nil
}

func refundSup() *Supervisor {
	s := New()
	s.Register(refundLimit{})
	s.Register(purchaseThreshold{})
	return s
}

func evalC(t *testing.T, s *Supervisor, constraint string, p ProposedAction, e Evidence) Decision {
	t.Helper()
	d, err := s.Evaluate(Request{Constraint: constraint, Scope: "txn-1", Proposed: p, Evidence: e, Budget: 4})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Verdict != Allow && d.Audit.Executed {
		t.Fatalf("INVARIANT: non-ALLOW %s marked executed", d.Verdict)
	}
	return d
}

// ---- applicability unit contract ----

func TestApplicability_RefundShapes(t *testing.T) {
	c := refundLimit{}
	cases := []struct {
		name string
		p    ProposedAction
		want Applicability
	}{
		{"correct refund shape", ProposedAction{Kind: "refund", Fields: map[string]any{"amount": 50.0}}, Applies},
		{"missing kind", ProposedAction{Fields: map[string]any{"amount": 50.0}}, CannotDetermine},
		{"refund missing amount", ProposedAction{Kind: "refund"}, CannotDetermine},
		{"unrelated action", ProposedAction{Kind: "search", Fields: map[string]any{"q": "x"}}, DoesNotApply},
	}
	for _, tc := range cases {
		if got := c.Applicable(tc.p, Evidence{}); got != tc.want {
			t.Fatalf("%s: Applicable=%s want %s", tc.name, got, tc.want)
		}
	}
}

// ---- kernel behavior for each applicability (the safety-critical part) ----

// ATTACK 1: a refund with a MISSING Kind must never ALLOW.
func TestRefund_MissingKind_NeverAllow(t *testing.T) {
	d := evalC(t, refundSup(), "refund_limit",
		ProposedAction{Fields: map[string]any{"amount": 5000.0}}, Evidence{})
	if d.Verdict != RequireEvidence {
		t.Fatalf("missing Kind must be REQUIRE_EVIDENCE (fail closed), got %s", d.Verdict)
	}
	if d.Verdict == Allow || d.Audit.Executed {
		t.Fatal("a $5000 refund with missing Kind must NEVER be ALLOWED/executed")
	}
}

func TestRefund_MissingAmount_NeverAllow(t *testing.T) {
	d := evalC(t, refundSup(), "refund_limit", ProposedAction{Kind: "refund"}, Evidence{})
	if d.Verdict != RequireEvidence {
		t.Fatalf("refund with missing amount must be REQUIRE_EVIDENCE, got %s", d.Verdict)
	}
}

// ATTACK 2: an unrelated action positively identified -> DOES_NOT_APPLY -> ALLOW.
func TestRefund_UnrelatedAction_DoesNotApply_Allows(t *testing.T) {
	d := evalC(t, refundSup(), "refund_limit",
		ProposedAction{Kind: "search", Fields: map[string]any{"q": "flights"}}, Evidence{})
	if d.Verdict != Allow {
		t.Fatalf("a positively-unrelated action must ALLOW (does not apply), got %s", d.Verdict)
	}
	if d.Audit.Applicable {
		t.Fatal("audit should mark this as not-applicable")
	}
}

func TestRefund_OverLimit_Rejects(t *testing.T) {
	d := evalC(t, refundSup(), "refund_limit",
		ProposedAction{Kind: "refund", Fields: map[string]any{"amount": 5000.0}}, Evidence{})
	if d.Verdict != Reject {
		t.Fatalf("$5000 over $500 must REJECT, got %s", d.Verdict)
	}
}

func TestRefund_WithinLimit_Allows(t *testing.T) {
	d := evalC(t, refundSup(), "refund_limit",
		ProposedAction{Kind: "refund", Fields: map[string]any{"amount": 100.0}}, Evidence{})
	if d.Verdict != Allow || !d.Audit.Executed {
		t.Fatalf("$100 within $500 must ALLOW+execute, got %s executed=%v", d.Verdict, d.Audit.Executed)
	}
}

// Second, non-rank constraint proves the contract is generic.
func TestPurchaseThreshold_Contract(t *testing.T) {
	s := refundSup()
	within := evalC(t, s, "purchase_threshold",
		ProposedAction{Kind: "purchase", Fields: map[string]any{"amount": 50.0}}, Evidence{RequestedRank: 100})
	if within.Verdict != Allow {
		t.Fatalf("purchase within threshold must ALLOW, got %s", within.Verdict)
	}
	over := evalC(t, s, "purchase_threshold",
		ProposedAction{Kind: "purchase", Fields: map[string]any{"amount": 5000.0}}, Evidence{RequestedRank: 100})
	if over.Verdict != Reject {
		t.Fatalf("purchase over threshold must REJECT, got %s", over.Verdict)
	}
	missingKind := evalC(t, s, "purchase_threshold",
		ProposedAction{Fields: map[string]any{"amount": 50.0}}, Evidence{RequestedRank: 100})
	if missingKind.Verdict != RequireEvidence {
		t.Fatalf("purchase with missing kind must REQUIRE_EVIDENCE, got %s", missingKind.Verdict)
	}
	unrelated := evalC(t, s, "purchase_threshold",
		ProposedAction{Kind: "deploy"}, Evidence{RequestedRank: 100})
	if unrelated.Verdict != Allow {
		t.Fatalf("non-purchase action must ALLOW (does not apply), got %s", unrelated.Verdict)
	}
}
