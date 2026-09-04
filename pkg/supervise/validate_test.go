package supervise

import "testing"

// validation now dispatches through the Supervisor: generic structural checks + the
// constraint's own EvidenceValidator (co-located, no central switch).

func TestValidateEvidence_RankRequiresRank(t *testing.T) {
	err := New().ValidateEvidence("rank", Evidence{Options: []Option{{ID: "A", Price: 1}}, RequestedRank: 0})
	if err == nil {
		t.Fatal("rank constraint with no requested_rank must be rejected as malformed")
	}
}

func TestValidateEvidence_EmptyOptionID(t *testing.T) {
	err := New().ValidateEvidence("rank", Evidence{Options: []Option{{ID: "", Price: 1}}, RequestedRank: 1})
	if err == nil {
		t.Fatal("an option with an empty id must be rejected")
	}
}

func TestValidateEvidence_DuplicateOptionID(t *testing.T) {
	err := New().ValidateEvidence("rank", Evidence{Options: []Option{{ID: "A", Price: 1}, {ID: "A", Price: 2}}, RequestedRank: 1})
	if err == nil {
		t.Fatal("duplicate option ids must be rejected")
	}
}

func TestValidateEvidence_NegativePrice(t *testing.T) {
	err := New().ValidateEvidence("rank", Evidence{Options: []Option{{ID: "A", Price: -5}}, RequestedRank: 1})
	if err == nil {
		t.Fatal("a negative price must be rejected")
	}
}

func TestValidateEvidence_ExpiresBeforeObserved(t *testing.T) {
	err := New().ValidateEvidence("rank", Evidence{Options: []Option{{ID: "A", Price: 1}}, RequestedRank: 1,
		Meta: EvidenceMeta{ObservedAtUnix: 100, ExpiresAtUnix: 50}})
	if err == nil {
		t.Fatal("expires_at before observed_at must be rejected")
	}
}

func TestValidateEvidence_ValidPasses(t *testing.T) {
	err := New().ValidateEvidence("rank", Evidence{Options: []Option{{ID: "A", Price: 163}, {ID: "B", Price: 290}},
		RequestedRank: 2, EvidenceComplete: true})
	if err != nil {
		t.Fatalf("well-formed rank evidence must validate; got %v", err)
	}
}

// Unknown constraint fails closed at validation too (defense in depth).
func TestValidateEvidence_UnknownConstraintRejected(t *testing.T) {
	if err := New().ValidateEvidence("nope", Evidence{Options: []Option{{ID: "A", Price: 1}}}); err == nil {
		t.Fatal("an unknown constraint must be rejected by ValidateEvidence")
	}
}

// Structural checks apply regardless of the constraint's own validator.
func TestValidateEvidence_StructuralAppliesToAnyRegistered(t *testing.T) {
	s := New()
	s.Register(refundLimit{}) // has no EvidenceValidator, so only structural checks apply
	if err := s.ValidateEvidence("refund_limit", Evidence{Options: []Option{{ID: "A", Price: 1}}}); err != nil {
		t.Fatalf("structurally valid evidence must pass for a constraint without a validator; got %v", err)
	}
	if err := s.ValidateEvidence("refund_limit", Evidence{Options: []Option{{ID: "", Price: 1}}}); err == nil {
		t.Fatal("structural checks must still apply for a constraint without its own validator")
	}
}

// A constraint's OWN validator runs (co-located requirement enforcement).
func TestValidateEvidence_ConstraintOwnValidatorRuns(t *testing.T) {
	s := New()
	s.Register(purchaseThreshold{}) // requires a positive threshold in evidence.RequestedRank (reused as the cap)
	if err := s.ValidateEvidence("purchase_threshold", Evidence{RequestedRank: 0}); err == nil {
		t.Fatal("purchase_threshold's own validator must reject a missing threshold")
	}
	if err := s.ValidateEvidence("purchase_threshold", Evidence{RequestedRank: 100}); err != nil {
		t.Fatalf("purchase_threshold with a threshold must validate; got %v", err)
	}
}
