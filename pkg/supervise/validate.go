package supervise

import (
	"fmt"
	"math"
	"strings"
)

// ValidateEvidence performs strict, fail-loud validation of an evidence payload BEFORE it
// reaches Evaluate. It exists so a malformed or typo'd payload cannot silently become Go zero
// values that produce ALLOW.
//
// Validation is split so it cannot be silently weakened when a constraint is added:
//   - generic STRUCTURAL checks (validateStructural) apply to every constraint;
//   - constraint-SPECIFIC required-field checks live ON the constraint (EvidenceValidator),
//     co-located with the code that depends on them — there is no central per-constraint
//     switch to forget.
//
// An unknown constraint is refused here too (defense in depth); it does NOT verify that the
// evidence is true — that is the caller's responsibility (see the trust boundary).
func (s *Supervisor) ValidateEvidence(constraint string, e Evidence) error {
	if err := validateStructural(e); err != nil {
		return err
	}
	c, ok := s.constraints[constraint]
	if !ok {
		return fmt.Errorf("unknown constraint %q", constraint)
	}
	if v, ok := c.(EvidenceValidator); ok {
		return v.ValidateEvidence(e)
	}
	return nil
}

// validateStructural applies the constraint-independent structural checks.
func validateStructural(e Evidence) error {
	seen := make(map[string]bool, len(e.Options))
	for i, o := range e.Options {
		if strings.TrimSpace(o.ID) == "" {
			return fmt.Errorf("option[%d] has an empty id", i)
		}
		if seen[o.ID] {
			return fmt.Errorf("duplicate option id %q", o.ID)
		}
		seen[o.ID] = true
		if math.IsNaN(o.Price) || math.IsInf(o.Price, 0) {
			return fmt.Errorf("option %q has a non-finite price", o.ID)
		}
		if o.Price < 0 {
			return fmt.Errorf("option %q has a negative price (%v)", o.ID, o.Price)
		}
	}

	if e.Meta.ObservedAtUnix < 0 || e.Meta.ExpiresAtUnix < 0 {
		return fmt.Errorf("evidence meta timestamps must be non-negative unix seconds")
	}
	if e.Meta.ObservedAtUnix > 0 && e.Meta.ExpiresAtUnix > 0 && e.Meta.ExpiresAtUnix < e.Meta.ObservedAtUnix {
		return fmt.Errorf("evidence meta expires_at is before observed_at")
	}
	return nil
}
