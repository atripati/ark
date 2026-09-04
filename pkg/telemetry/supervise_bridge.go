package telemetry

import "github.com/atripati/ark/pkg/supervise"

// SupervisionFromDecision maps a pkg/supervise verdict into the telemetry Supervision
// block, so a supervised action lives in the SAME DecisionRecord as everything else.
// It records only the judgment + structured, SAFE references (fingerprints, ids, provenance)
// to the action and evidence — never the raw evidence, which may be large or sensitive. The
// supervision semantics are unchanged — this is read-only observability over an existing
// decision.
//
// evidenceRef is a caller-supplied reference for the evidence snapshot (an evidence_id or a
// content fingerprint). It replaces the former meaningless constant so an auditor can tie the
// verdict to the exact evidence state that produced it.
func SupervisionFromDecision(d supervise.Decision, evidenceRef string) *Supervision {
	a := d.Audit
	retry := a.RetryNumber
	executed := a.Executed
	return &Supervision{
		ApplicableConstraint:   a.Constraint,
		Scope:                  a.Scope,
		Verdict:                string(d.Verdict),
		ProposedOption:         a.Proposed.Option,
		ProposedKind:           a.Proposed.Kind,
		ProposedFingerprint:    a.ProposedFingerprint,
		TrustedEvidenceRef:     evidenceRef,
		EvidenceFingerprint:    a.EvidenceFingerprint,
		EvidenceSource:         a.EvidenceUsed.Meta.Source,
		EvidenceVersion:        a.EvidenceUsed.Meta.Version,
		EvidenceObservedAtUnix: a.EvidenceUsed.Meta.ObservedAtUnix,
		RejectionReason:        a.RejectionReason,
		RetryNumber:            &retry,
		SuggestedFromEvidence:  a.SuggestedFromEvidence,
		Executed:               &executed,
	}
}
