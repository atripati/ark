// Package supervise is an EXPERIMENTAL, domain-agnostic mechanism that supervises an
// agent's proposed actions against a runtime-derived constraint, without ever authoring
// the action itself. See SPEC.md. Off by default; opt-in only.
//
// It contains no task, benchmark, or domain constants. Domain code supplies the evidence
// and (optionally) constraints; the mechanism only judges and gates agent-authored actions.
package supervise

import "sort"

// Verdict is the outcome of one supervision evaluation.
type Verdict string

const (
	Allow             Verdict = "ALLOW"              // not applicable, or the action satisfies the constraint
	Reject            Verdict = "REJECT"             // the action provably violates the constraint
	RequireEvidence   Verdict = "REQUIRE_EVIDENCE"   // applies, but evidence is insufficient to validate
	RecoveryExhausted Verdict = "RECOVERY_EXHAUSTED" // retry budget spent while still unsatisfied
)

// ProposedAction is the agent-authored action under supervision. Fields are opaque to the
// mechanism; a constraint interprets them.
type ProposedAction struct {
	Kind   string         `json:"kind,omitempty"`
	Option string         `json:"option,omitempty"` // opaque id of the chosen option/itinerary
	Fields map[string]any `json:"fields,omitempty"`
}

// Option is one retrieved, priced candidate. Generic: no domain meaning attached.
type Option struct {
	ID       string  `json:"id"`
	Price    float64 `json:"price"`
	IsDirect bool    `json:"is_direct,omitempty"`
}

// Evidence is trusted runtime facts the caller gathered — never the agent's own claims,
// never task gold or evaluator data.
type Evidence struct {
	Options          []Option `json:"options,omitempty"`
	RequestedRank    int      `json:"requested_rank,omitempty"` // 0 = no rank preference (not applicable)
	NonstopOnly      bool     `json:"nonstop_only,omitempty"`
	EvidenceComplete bool     `json:"evidence_complete,omitempty"` // caller asserts the option set is complete
}

// Request is a single supervision evaluation. RetryCount is owned by the caller (the loop
// state); Budget bounds the constrained retry.
type Request struct {
	Constraint string         `json:"constraint"`
	Proposed   ProposedAction `json:"proposed"`
	Evidence   Evidence       `json:"evidence"`
	RetryCount int            `json:"retry_count"`
	Budget     int            `json:"budget"`
}

// AuditRecord captures everything about one intervention (SPEC "Auditability").
type AuditRecord struct {
	Constraint      string         `json:"constraint"`
	Applicable      bool           `json:"applicable"`
	Proposed        ProposedAction `json:"proposed"`
	EvidenceUsed    Evidence       `json:"evidence_used"`
	Verdict         Verdict        `json:"verdict"`
	RejectionReason string         `json:"rejection_reason,omitempty"`
	RetryNumber     int            `json:"retry_number"`
	// SuggestedFromEvidence is the runtime-derived target the rejection points at. It is
	// EVIDENCE returned to the agent, NOT an action authored on its behalf — the agent must
	// still propose the next action itself.
	SuggestedFromEvidence string `json:"suggested_from_evidence,omitempty"`
	Executed              bool   `json:"executed"`
}

// Decision is the mechanism's output for one evaluation.
type Decision struct {
	Verdict Verdict     `json:"verdict"`
	Reason  string      `json:"reason,omitempty"`
	Audit   AuditRecord `json:"audit"`
}

// Constraint is the generic hook domain code implements. The mechanism knows nothing about
// the domain; it only calls Applicable/Validate.
type Constraint interface {
	Name() string
	Applicable(p ProposedAction, e Evidence) bool
	// Validate returns a base verdict (Allow / Reject / RequireEvidence), a human reason,
	// and an optional runtime-derived suggestion (evidence, not an authored action).
	Validate(p ProposedAction, e Evidence) (Verdict, string, string)
}

// Supervisor runs evaluations against registered constraints.
type Supervisor struct {
	constraints map[string]Constraint
}

// New returns a Supervisor with the built-in generic constraints registered.
func New() *Supervisor {
	s := &Supervisor{constraints: map[string]Constraint{}}
	s.Register(RankConstraint{})
	return s
}

// Register adds a constraint under its Name().
func (s *Supervisor) Register(c Constraint) { s.constraints[c.Name()] = c }

// Evaluate runs one supervision step. The mechanism NEVER constructs the action: it only
// returns a verdict (+ evidence). RECOVERY_EXHAUSTED replaces REJECT/REQUIRE_EVIDENCE once the
// budget is spent, so a known-violating action is never executed after the budget.
func (s *Supervisor) Evaluate(req Request) Decision {
	a := AuditRecord{Constraint: req.Constraint, Proposed: req.Proposed,
		EvidenceUsed: req.Evidence, RetryNumber: req.RetryCount}

	c, ok := s.constraints[req.Constraint]
	if !ok || !c.Applicable(req.Proposed, req.Evidence) {
		a.Applicable = ok && c.Applicable(req.Proposed, req.Evidence)
		a.Verdict, a.Executed = Allow, true
		reason := "constraint not applicable"
		if !ok {
			reason = "unknown constraint; allow"
		}
		return Decision{Verdict: Allow, Reason: reason, Audit: a}
	}
	a.Applicable = true

	v, reason, suggested := c.Validate(req.Proposed, req.Evidence)
	a.SuggestedFromEvidence = suggested
	if v == Allow {
		a.Verdict, a.Executed = Allow, true
		return Decision{Verdict: Allow, Reason: reason, Audit: a}
	}
	// v is Reject or RequireEvidence: apply the bounded-retry budget.
	if req.Budget > 0 && req.RetryCount >= req.Budget {
		a.Verdict, a.RejectionReason, a.Executed = RecoveryExhausted, reason, false
		return Decision{Verdict: RecoveryExhausted, Reason: "retry budget exhausted: " + reason, Audit: a}
	}
	a.Verdict, a.RejectionReason, a.Executed = v, reason, false
	return Decision{Verdict: v, Reason: reason, Audit: a}
}

// ---- small generic price helpers (used by the rank constraint) ----

func distinctSortedPrices(opts []Option) []float64 {
	seen := map[float64]bool{}
	out := []float64{}
	for _, o := range opts {
		if !seen[o.Price] {
			seen[o.Price] = true
			out = append(out, o.Price)
		}
	}
	sort.Float64s(out)
	return out
}

func indexOfPrice(tiers []float64, p float64) int {
	for i, t := range tiers {
		if t == p {
			return i
		}
	}
	return -1
}

func priceOfOption(id string, opts []Option) (float64, bool) {
	for _, o := range opts {
		if o.ID == id {
			return o.Price, true
		}
	}
	return 0, false
}

func firstOptionAtPrice(opts []Option, p float64) string {
	best := ""
	for _, o := range opts {
		if o.Price == p && (best == "" || o.ID < best) {
			best = o.ID
		}
	}
	return best
}
