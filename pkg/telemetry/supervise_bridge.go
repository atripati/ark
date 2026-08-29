package telemetry

import "github.com/atripati/ark/pkg/supervise"

// SupervisionFromDecision maps a pkg/supervise verdict into the telemetry Supervision
// block, so a supervised action lives in the SAME DecisionRecord as everything else.
// It records only the judgment + a REFERENCE to the trusted evidence (never the raw
// evidence, which may be large or sensitive). The supervision semantics are unchanged —
// this is read-only observability over an existing decision.
func SupervisionFromDecision(d supervise.Decision, evidenceRef string) *Supervision {
	a := d.Audit
	retry := a.RetryNumber
	executed := a.Executed
	return &Supervision{
		ApplicableConstraint:  a.Constraint,
		Verdict:               string(d.Verdict),
		TrustedEvidenceRef:    evidenceRef, // caller supplies a reference/id, not the raw bag
		RejectionReason:       a.RejectionReason,
		RetryNumber:           &retry,
		SuggestedFromEvidence: a.SuggestedFromEvidence,
		Executed:              &executed,
	}
}
