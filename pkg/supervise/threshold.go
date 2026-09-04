package supervise

import "fmt"

// ThresholdConstraint is a generic, PROTECTED money/limit constraint: the proposed action's
// numeric `amount` (AGENT-authored) must not exceed a `limit` that a TRUSTED PROVIDER
// established as a fact. The separation is the whole point — the agent proposes the amount, but
// it cannot raise its own ceiling, because the limit comes through the trusted evidence channel.
//
// It requires trusted-provider evidence (RequiresTrustedProvider), so agent- or caller-supplied
// evidence can never satisfy it. It is domain-neutral: the same constraint expresses refund
// limits, purchase thresholds, spend caps, etc. — the meaning is the caller's `limit` fact.
type ThresholdConstraint struct{}

func (ThresholdConstraint) Name() string { return "threshold" }

// RequiresTrustedProvider marks this constraint PROTECTED: only trusted-provider evidence can
// authorize it.
func (ThresholdConstraint) RequiresTrustedProvider() bool { return true }

// Applicable: the constraint governs any action that carries a numeric amount to bound. A
// missing amount is CannotDetermine (fail closed), never a silent pass.
func (ThresholdConstraint) Applicable(p ProposedAction, _ Evidence) Applicability {
	if _, ok := numeric(p.Fields["amount"]); !ok {
		return CannotDetermine
	}
	return Applies
}

func (ThresholdConstraint) Validate(p ProposedAction, e Evidence) (Verdict, string, string) {
	amt, ok := numeric(p.Fields["amount"])
	if !ok {
		return RequireEvidence, "proposed amount is missing/unparseable", ""
	}
	limit, ok := e.Facts["limit"]
	if !ok {
		return RequireEvidence, "trusted evidence does not include the required 'limit' fact", ""
	}
	if amt > limit {
		return Reject, fmt.Sprintf("proposed amount %.2f exceeds the trusted limit %.2f", amt, limit), ""
	}
	return Allow, fmt.Sprintf("proposed amount %.2f is within the trusted limit %.2f", amt, limit), ""
}

// (No ValidateEvidence: a missing 'limit' fact is INSUFFICIENT evidence, not malformed
// configuration — Validate returns REQUIRE_EVIDENCE so the caller can re-resolve, rather than a
// hard bridge error.)

// numeric coerces a JSON-decoded value (float64) or an int into a float64.
func numeric(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}
