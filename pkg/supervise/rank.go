package supervise

import "fmt"

// RankConstraint is the single generic constraint shipped to exercise the mechanism:
// "the proposed option must be the rank-N cheapest within the complete set of retrieved,
// priced options." It is a faithful generalization of the proven research constraint —
// it operates only on opaque option ids + numeric prices + a requested rank + an
// evidence-completeness flag. No domain values, task ids, or gold are baked in.
type RankConstraint struct{}

func (RankConstraint) Name() string { return "rank" }

// Applicable: whenever a caller routes an action through the rank constraint, rank governs it,
// so it always APPLIES. Presence and validity of requested_rank is enforced by ValidateEvidence
// (at the trust boundary) and by Validate — a missing/invalid rank must FAIL CLOSED
// (error / REQUIRE_EVIDENCE), never silently ALLOW via an "inapplicable" determination.
func (RankConstraint) Applicable(_ ProposedAction, _ Evidence) Applicability { return Applies }

// ValidateEvidence declares rank's required fields, co-located with the constraint (satisfies
// EvidenceValidator). A rank check without a valid requested_rank is malformed configuration.
func (RankConstraint) ValidateEvidence(e Evidence) error {
	if e.RequestedRank < 1 {
		return fmt.Errorf("the rank constraint requires requested_rank >= 1 (got %d)", e.RequestedRank)
	}
	return nil
}

// Validate mirrors the frozen research predicate, hardened to fail closed:
//   - requested_rank must be >= 1, else REQUIRE_EVIDENCE (a missing rank is not "no preference");
//   - the relevant candidate set must be asserted complete (for both general and nonstop-only
//     requests), else REQUIRE_EVIDENCE — a rank cannot be verified against a partial set;
//   - build candidates from the options (filtered to direct-only iff nonstop-only);
//   - if the rank cannot be established from the evidence (no candidates, rank exceeds the
//     established tiers, the proposed option is absent or unpriceable) -> REQUIRE_EVIDENCE.
//     ARK never ALLOWs merely because verification failed, and never invents the answer;
//   - if the proposed option's price tier == the requested rank -> ALLOW;
//   - otherwise REJECT, returning the runtime-derived rank-N option id as evidence.
func (RankConstraint) Validate(p ProposedAction, e Evidence) (Verdict, string, string) {
	n := e.RequestedRank
	if n < 1 {
		return RequireEvidence,
			"the rank constraint requires requested_rank >= 1, but none was provided; supply the requested rank as trusted evidence",
			""
	}

	// A rank over a price-ordered set can only be verified against the COMPLETE relevant set.
	// Without an explicit completeness assertion, ARK cannot prove the proposal is rank-N.
	if !e.EvidenceComplete {
		return RequireEvidence,
			"the candidate set is not asserted complete; gather the complete relevant option set (all direct options when nonstop-only), then re-propose the requested rank",
			""
	}

	cands := make([]Option, 0, len(e.Options))
	for _, o := range e.Options {
		if e.NonstopOnly && !o.IsDirect {
			continue
		}
		cands = append(cands, o)
	}
	if len(cands) == 0 {
		return RequireEvidence,
			"no rankable candidates in the evidence; cannot verify the requested rank",
			""
	}

	tiers := distinctSortedPrices(cands)
	if n > len(tiers) {
		return RequireEvidence,
			fmt.Sprintf("requested rank %d exceeds the %d distinct price tiers established by the evidence; gather more candidates or correct the rank", n, len(tiers)),
			""
	}

	pp, ok := priceOfOption(p.Option, e.Options)
	if !ok {
		return RequireEvidence,
			fmt.Sprintf("proposed option %q is absent from the retrieved evidence; cannot verify its rank", p.Option),
			""
	}
	if indexOfPrice(tiers, pp) < 0 {
		return RequireEvidence,
			fmt.Sprintf("proposed option %q is not priceable within the candidate tiers; cannot verify its rank", p.Option),
			""
	}

	proposedRank := indexOfPrice(tiers, pp) + 1
	if proposedRank == n {
		return Allow, fmt.Sprintf("proposed option is rank %d (%.0f) — matches requested rank %d", proposedRank, pp, n), ""
	}

	target := firstOptionAtPrice(cands, tiers[n-1])
	return Reject,
		fmt.Sprintf("proposed option %q is rank %d (%.0f); the requested rank-%d option in the retrieved evidence is %q (%.0f) — re-propose that option",
			p.Option, proposedRank, pp, n, target, tiers[n-1]),
		target
}
