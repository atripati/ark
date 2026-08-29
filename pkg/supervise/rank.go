package supervise

import "fmt"

// RankConstraint is the single generic constraint shipped to exercise the mechanism:
// "the proposed option must be the rank-N cheapest within the complete set of retrieved,
// priced options." It is a faithful generalization of the proven research constraint —
// it operates only on opaque option ids + numeric prices + a requested rank + an
// evidence-completeness flag. No domain values, task ids, or gold are baked in.
type RankConstraint struct{}

func (RankConstraint) Name() string { return "rank" }

// Applicable: a rank preference must have been expressed (RequestedRank >= 1).
func (RankConstraint) Applicable(_ ProposedAction, e Evidence) bool {
	return e.RequestedRank >= 1
}

// Validate mirrors the frozen research predicate (post filter-fix):
//   - connecting/other options are relevant unless the request is nonstop-only, so an
//     incomplete option set yields REQUIRE_EVIDENCE;
//   - build the candidate set from ALL options (filtered to direct-only iff nonstop-only);
//   - if the requested rank cannot be established from the evidence, ALLOW (do not block on
//     insufficient/degenerate evidence — the mechanism never invents an answer);
//   - if the proposed option's price tier == the requested rank, ALLOW;
//   - otherwise REJECT, returning the runtime-derived rank-N option id as evidence.
func (RankConstraint) Validate(p ProposedAction, e Evidence) (Verdict, string, string) {
	n := e.RequestedRank

	if !e.NonstopOnly && !e.EvidenceComplete {
		return RequireEvidence,
			"a price-ranked option was requested, but the option set is incomplete; gather the remaining options, then re-propose the requested rank",
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
		return Allow, "no rankable candidates in the evidence; cannot verify rank", ""
	}

	tiers := distinctSortedPrices(cands)
	if n > len(tiers) {
		return Allow, fmt.Sprintf("requested rank %d exceeds %d retrieved price tiers", n, len(tiers)), ""
	}

	pp, ok := priceOfOption(p.Option, e.Options)
	if !ok || indexOfPrice(tiers, pp) < 0 {
		return Allow, "proposed option is not priceable from the retrieved evidence; cannot verify rank", ""
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
