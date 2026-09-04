// Package supervise is an EXPERIMENTAL, domain-agnostic mechanism that supervises an
// agent's proposed actions against a runtime-derived constraint, without ever authoring
// the action itself. See SPEC.md. Off by default; opt-in only.
//
// It contains no task, benchmark, or domain constants. Domain code supplies the evidence
// and (optionally) constraints; the mechanism only judges and gates agent-authored actions.
//
// Safety posture: FAIL CLOSED. ARK never returns ALLOW for a supervised action merely
// because the constraint is unknown, the evidence is malformed/incomplete, the proposal
// cannot be located, or validity cannot otherwise be established. When ARK cannot prove the
// proposal satisfies an applicable constraint, the verdict is REQUIRE_EVIDENCE (or a
// fail-closed error for an unknown/misconfigured constraint) — never ALLOW.
package supervise

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// Verdict is the outcome of one supervision evaluation.
type Verdict string

const (
	Allow             Verdict = "ALLOW"              // the action provably satisfies the constraint, or the constraint provably does not apply
	Reject            Verdict = "REJECT"             // the action provably violates the constraint
	RequireEvidence   Verdict = "REQUIRE_EVIDENCE"   // the constraint applies but validity cannot be established from the evidence
	RecoveryExhausted Verdict = "RECOVERY_EXHAUSTED" // retry budget spent while still unsatisfied
)

// ErrUnknownConstraint is returned by Evaluate when the requested constraint is not
// registered. It is a fail-CLOSED configuration error: ARK must never authorize execution
// under a constraint it does not implement. Callers must treat it as a hard error, not as a
// verdict the agent can recover from.
var ErrUnknownConstraint = errors.New("supervise: unknown constraint")

// Applicability is a constraint's determination of whether it governs a proposed action. It is
// a THREE-state result on purpose: a boolean conflates "does not apply" with "cannot tell",
// and the kernel would fail OPEN on the latter. Missing/malformed data must yield
// CannotDetermine, never DoesNotApply.
type Applicability string

const (
	// Applies: the constraint positively governs this action; the kernel calls Validate.
	Applies Applicability = "APPLIES"
	// DoesNotApply: the constraint POSITIVELY determined it does not govern this action. Only
	// this — never uncertainty — permits the kernel to ALLOW on applicability grounds.
	DoesNotApply Applicability = "DOES_NOT_APPLY"
	// CannotDetermine: applicability cannot be established (e.g. a required field is missing).
	// The kernel treats this as REQUIRE_EVIDENCE and never ALLOWs.
	CannotDetermine Applicability = "CANNOT_DETERMINE"
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

// EvidenceTrust classifies HOW the evidence reached ARK — the separation of trust paths at the
// heart of the trusted-evidence plane. ARK still cannot prove the facts are TRUE (a provider can
// itself be wrong/compromised); it can prove which trust CHANNEL the facts came through.
type EvidenceTrust string

const (
	// TrustAgentSupplied: evidence originated from agent-controlled context. Never sufficient for
	// a protected constraint.
	TrustAgentSupplied EvidenceTrust = "AGENT_SUPPLIED"
	// TrustCallerSupplied: the integration passed evidence directly (may or may not be agent-
	// derived — ARK cannot tell). The legacy/weaker mode.
	TrustCallerSupplied EvidenceTrust = "CALLER_SUPPLIED"
	// TrustProvider: evidence was resolved through an application-configured EvidenceProvider the
	// agent cannot select or influence. Required for a protected constraint.
	TrustProvider EvidenceTrust = "TRUSTED_PROVIDER"
)

// EvidenceMeta is domain-agnostic provenance/freshness metadata for one evidence snapshot, plus
// the trust-plane binding. ARK RECORDS it, ENFORCES freshness + trust + binding — but does not
// prove the evidence is TRUE (see SPEC "Trust boundary").
type EvidenceMeta struct {
	EvidenceID     string `json:"evidence_id,omitempty"`      // stable id for this snapshot
	Source         string `json:"source,omitempty"`           // provenance identifier (which system produced it)
	Version        string `json:"version,omitempty"`          // version/etag of the snapshot
	ObservedAtUnix int64  `json:"observed_at_unix,omitempty"` // when the evidence was observed/fetched; 0 = not provided
	ExpiresAtUnix  int64  `json:"expires_at_unix,omitempty"`  // hard expiry; 0 = no explicit expiry

	// --- trusted-evidence plane ---
	Trust      EvidenceTrust `json:"trust,omitempty"`       // how the evidence reached ARK
	ProviderID string        `json:"provider_id,omitempty"` // the configured provider that resolved it (trust=provider)
	Subject    string        `json:"subject,omitempty"`     // the entity the evidence is ABOUT (must match scope)
	Namespace  string        `json:"namespace,omitempty"`   // tenant/application the evidence belongs to
	// RequestFingerprint binds the envelope to the exact question it answered (namespace,
	// transaction, scope, constraint). ARK recomputes it and refuses a mismatch, so evidence
	// resolved for one subject/transaction/policy cannot be reused for another.
	RequestFingerprint string `json:"request_fingerprint,omitempty"`
	// attestation seam (future PKI) — recorded, not yet verified by the kernel.
	Attestation string `json:"attestation,omitempty"`
	Issuer      string `json:"issuer,omitempty"`
	KeyID       string `json:"key_id,omitempty"`
}

// Evidence is trusted runtime facts the caller gathered — never the agent's own claims,
// never task gold or evaluator data. Facts carries domain-neutral numeric facts a trusted
// provider established (e.g. a limit, threshold, balance) for generic threshold-style constraints.
type Evidence struct {
	Options          []Option           `json:"options,omitempty"`
	RequestedRank    int                `json:"requested_rank,omitempty"` // 0 = not provided (fails closed for the rank constraint)
	NonstopOnly      bool               `json:"nonstop_only,omitempty"`
	EvidenceComplete bool               `json:"evidence_complete,omitempty"` // caller asserts the relevant candidate set is complete
	Facts            map[string]float64 `json:"facts,omitempty"`             // trusted numeric facts (limit/threshold/balance/...)
	Meta             EvidenceMeta       `json:"meta,omitempty"`
}

// FreshnessPolicy optionally requires the evidence to be recent. Zero value = no freshness
// requirement (backward compatible). When MaxAgeSec > 0 (or the evidence carries an ExpiresAt),
// evidence with no observed_at, older than MaxAgeSec, past its ExpiresAt, or timestamped in the
// future beyond SkewSec yields REQUIRE_EVIDENCE. SkewSec bounds acceptable clock skew so a
// far-future observed_at cannot masquerade as fresh.
type FreshnessPolicy struct {
	MaxAgeSec int64 `json:"max_age_sec,omitempty"`
	SkewSec   int64 `json:"skew_sec,omitempty"`
}

// CheckFreshness is the single deterministic freshness predicate, shared by the kernel (at
// check time) and the bridge's pre-execution consume gate (at use time). It takes explicit
// unix seconds so it never reads a wall clock itself. A far-future observed_at (beyond skewSec)
// can never be treated as fresh.
func CheckFreshness(observedAtUnix, expiresAtUnix, nowUnix, maxAgeSec, skewSec int64) (bool, string) {
	if observedAtUnix <= 0 {
		return false, "freshness is required but the evidence carries no observed_at timestamp"
	}
	if skewSec < 0 {
		skewSec = 0
	}
	if nowUnix > 0 && observedAtUnix > nowUnix+skewSec {
		return false, fmt.Sprintf("evidence observed_at is %ds in the future (beyond the %ds allowed clock skew); cannot establish freshness",
			observedAtUnix-nowUnix, skewSec)
	}
	if maxAgeSec > 0 && nowUnix > 0 && nowUnix-observedAtUnix > maxAgeSec {
		return false, fmt.Sprintf("evidence is stale: observed %ds ago, but freshness requires <= %ds", nowUnix-observedAtUnix, maxAgeSec)
	}
	if expiresAtUnix > 0 && nowUnix > 0 && nowUnix >= expiresAtUnix {
		return false, "evidence has expired (expires_at has passed)"
	}
	return true, ""
}

// Request is a single supervision evaluation. RetryCount is owned by the caller (the loop
// state); Budget bounds the constrained retry. Scope binds this evaluation to the exact
// transaction/entity being supervised (recorded in the audit; the caller keys retry state by
// it). NowUnix is injected so Evaluate stays a pure, deterministic function of its inputs.
type Request struct {
	Constraint  string          `json:"constraint"`
	Namespace   string          `json:"namespace,omitempty"`   // tenant/application (trust-plane binding)
	Transaction string          `json:"transaction,omitempty"` // authorization lifecycle (trust-plane binding)
	Scope       string          `json:"scope,omitempty"`
	Proposed    ProposedAction  `json:"proposed"`
	Evidence    Evidence        `json:"evidence"`
	RetryCount  int             `json:"retry_count"`
	Budget      int             `json:"budget"`
	NowUnix     int64           `json:"now_unix,omitempty"`
	Freshness   FreshnessPolicy `json:"freshness,omitempty"`
	// RequireTrustedProvider, set by the trusted integration (or implied by the constraint),
	// makes ARK refuse to ALLOW unless the evidence came through a configured provider.
	RequireTrustedProvider bool `json:"require_trusted_provider,omitempty"`
}

// RequestFingerprint canonically identifies the QUESTION evidence must answer: the (namespace,
// transaction, scope, constraint) tuple. A provider stamps it on the envelope it returns; ARK
// recomputes it and refuses evidence whose fingerprint does not match — so evidence resolved for
// one subject/transaction/policy can never be reused for another.
//
// Each field is hex-encoded before joining, so the canonical form is unambiguous and byte-for-byte
// identical in Go and in the Python SDK (no JSON-escaping divergence).
func RequestFingerprint(namespace, transaction, scope, constraint string) string {
	s := "ark-evreq-v1"
	for _, p := range []string{namespace, transaction, scope, constraint} {
		s += ":" + hex.EncodeToString([]byte(p))
	}
	h := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(h[:])
}

// TrustedProviderRequirer is an OPTIONAL interface a Constraint may implement to declare that it
// is PROTECTED: it can only ALLOW when the evidence came through a trusted provider. A request
// may also demand this per-call; the effective requirement is the OR of the two.
type TrustedProviderRequirer interface {
	RequiresTrustedProvider() bool
}

// AuditRecord captures everything about one intervention (SPEC "Auditability"). Fingerprints
// bind the verdict to the exact action and evidence state it evaluated, so a later execution
// of a materially different action is not implicitly authorized by this verdict.
type AuditRecord struct {
	Constraint          string         `json:"constraint"`
	Scope               string         `json:"scope,omitempty"`
	Applicable          bool           `json:"applicable"`
	Proposed            ProposedAction `json:"proposed"`
	ProposedFingerprint string         `json:"proposed_fingerprint,omitempty"`
	EvidenceUsed        Evidence       `json:"evidence_used"`
	EvidenceFingerprint string         `json:"evidence_fingerprint,omitempty"`
	Verdict             Verdict        `json:"verdict"`
	RejectionReason     string         `json:"rejection_reason,omitempty"`
	RetryNumber         int            `json:"retry_number"`
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
//
// Applicable returns an Applicability (three-state). Return DoesNotApply ONLY as a positive,
// trusted determination that this constraint does not govern the action. If a required field
// for that determination is missing/malformed, return CannotDetermine (the kernel fails
// closed to REQUIRE_EVIDENCE) — NEVER DoesNotApply, which would ALLOW. When the constraint is
// the explicitly selected one and simply needs more evidence to judge, prefer Applies and let
// Validate return RequireEvidence.
type Constraint interface {
	Name() string
	Applicable(p ProposedAction, e Evidence) Applicability
	// Validate returns a base verdict (Allow / Reject / RequireEvidence), a human reason,
	// and an optional runtime-derived suggestion (evidence, not an authored action). It must
	// return Allow ONLY when the proposal is provably valid; any inability to establish
	// validity must be RequireEvidence, never Allow.
	Validate(p ProposedAction, e Evidence) (Verdict, string, string)
}

// EvidenceValidator is an OPTIONAL interface a Constraint may implement to declare its own
// required-field validation, co-located with the constraint. The Supervisor calls it during
// ValidateEvidence after the generic structural checks. Keeping validation on the constraint
// means adding a constraint cannot silently weaken authorization by forgetting a central
// switch.
type EvidenceValidator interface {
	ValidateEvidence(e Evidence) error
}

// Supervisor runs evaluations against registered constraints.
type Supervisor struct {
	constraints map[string]Constraint
}

// New returns a Supervisor with the built-in generic constraints registered.
func New() *Supervisor {
	s := &Supervisor{constraints: map[string]Constraint{}}
	s.Register(RankConstraint{})
	s.Register(ThresholdConstraint{})
	return s
}

// Register adds a constraint under its Name().
func (s *Supervisor) Register(c Constraint) { s.constraints[c.Name()] = c }

// Has reports whether a constraint with this name is registered. Callers should use it to
// fail closed BEFORE evaluating, so an unknown constraint is reported as a configuration
// error rather than reaching the agent as a verdict.
func (s *Supervisor) Has(name string) bool { _, ok := s.constraints[name]; return ok }

// Evaluate runs one supervision step. The mechanism NEVER constructs the action: it only
// returns a verdict (+ evidence).
//
// FAIL CLOSED:
//   - unknown constraint            -> ErrUnknownConstraint, non-ALLOW decision (never executes)
//   - freshness required but stale  -> REQUIRE_EVIDENCE
//   - validity cannot be proven     -> REQUIRE_EVIDENCE (from the constraint)
//   - provable violation            -> REJECT
//   - budget spent while non-ALLOW  -> RECOVERY_EXHAUSTED (never executes)
//   - provably valid / provably N/A -> ALLOW
func (s *Supervisor) Evaluate(req Request) (Decision, error) {
	a := AuditRecord{
		Constraint: req.Constraint, Scope: req.Scope, Proposed: req.Proposed,
		ProposedFingerprint: Fingerprint(req.Proposed),
		EvidenceUsed:        req.Evidence,
		EvidenceFingerprint: Fingerprint(req.Evidence),
		RetryNumber:         req.RetryCount,
	}

	c, ok := s.constraints[req.Constraint]
	if !ok {
		// Fail closed: an unknown constraint must never authorize execution. Leave the verdict
		// empty (definitively not ALLOW) and return an error the caller must handle.
		a.Verdict, a.Executed = "", false
		return Decision{Verdict: "", Reason: "unknown constraint: " + req.Constraint, Audit: a},
			fmt.Errorf("%w: %q", ErrUnknownConstraint, req.Constraint)
	}

	switch c.Applicable(req.Proposed, req.Evidence) {
	case DoesNotApply:
		// POSITIVE determination that the constraint does not govern this action -> ALLOW.
		a.Applicable = false
		a.Verdict, a.Executed = Allow, true
		return Decision{Verdict: Allow, Reason: "constraint positively does not apply to this action", Audit: a}, nil
	case CannotDetermine:
		// Applicability could not be established (e.g. a required field is missing). FAIL
		// CLOSED: never ALLOW on uncertainty. Treated as insufficient evidence, bounded by the
		// retry budget so it can still terminate at RECOVERY_EXHAUSTED.
		a.Applicable = false
		return s.bounded(a, RequireEvidence,
			"constraint applicability could not be determined from the proposed action/evidence; supply the missing fields", "", req), nil
	case Applies:
		a.Applicable = true
	default:
		// A constraint returning an unrecognized applicability is a programming error; fail closed.
		a.Applicable = false
		return s.bounded(a, RequireEvidence, "constraint returned an unrecognized applicability; refusing (fail closed)", "", req), nil
	}

	// --- trusted-evidence plane: for a PROTECTED constraint, require evidence that came through a
	// configured provider AND is bound to THIS request/subject/tenant. Any shortfall fails closed
	// to REQUIRE_EVIDENCE — agent- or caller-supplied evidence, or evidence resolved for another
	// question, can never authorize a protected action.
	required := req.RequireTrustedProvider
	if tr, ok := c.(TrustedProviderRequirer); ok && tr.RequiresTrustedProvider() {
		required = true
	}
	if required {
		m := req.Evidence.Meta
		switch {
		case m.Trust != TrustProvider:
			return s.bounded(a, RequireEvidence,
				"this constraint requires evidence from a trusted provider; agent- or caller-supplied evidence cannot authorize it", "", req), nil
		case m.RequestFingerprint != RequestFingerprint(req.Namespace, req.Transaction, req.Scope, req.Constraint):
			return s.bounded(a, RequireEvidence,
				"trusted evidence was resolved for a different request (namespace/transaction/scope/constraint); re-resolve for this request", "", req), nil
		case req.Scope != "" && m.Subject != "" && m.Subject != req.Scope:
			return s.bounded(a, RequireEvidence,
				fmt.Sprintf("trusted evidence subject %q does not match the action scope %q", m.Subject, req.Scope), "", req), nil
		case m.Namespace != req.Namespace:
			return s.bounded(a, RequireEvidence,
				"trusted evidence belongs to a different namespace/tenant", "", req), nil
		}
	}

	// Freshness precondition (when a max-age policy is set OR the evidence declares an expiry).
	// If the constraint applies but the required freshness cannot be established, ARK cannot
	// prove validity — REQUIRE_EVIDENCE.
	if req.Freshness.MaxAgeSec > 0 || req.Evidence.Meta.ExpiresAtUnix > 0 {
		if fresh, why := CheckFreshness(req.Evidence.Meta.ObservedAtUnix, req.Evidence.Meta.ExpiresAtUnix,
			req.NowUnix, req.Freshness.MaxAgeSec, req.Freshness.SkewSec); !fresh {
			return s.bounded(a, RequireEvidence, why, "", req), nil
		}
	}

	v, reason, suggested := c.Validate(req.Proposed, req.Evidence)
	a.SuggestedFromEvidence = suggested
	if v == Allow {
		a.Verdict, a.Executed = Allow, true
		return Decision{Verdict: Allow, Reason: reason, Audit: a}, nil
	}
	// v is Reject or RequireEvidence: apply the bounded-retry budget.
	return s.bounded(a, v, reason, suggested, req), nil
}

// bounded turns a non-ALLOW base verdict into the final verdict, applying the retry budget.
// Once the budget is spent, RECOVERY_EXHAUSTED replaces REJECT/REQUIRE_EVIDENCE so a
// still-unsatisfied action is never executed after the budget.
func (s *Supervisor) bounded(a AuditRecord, v Verdict, reason, suggested string, req Request) Decision {
	a.SuggestedFromEvidence = suggested
	if req.Budget > 0 && req.RetryCount >= req.Budget {
		a.Verdict, a.RejectionReason, a.Executed = RecoveryExhausted, reason, false
		return Decision{Verdict: RecoveryExhausted, Reason: "retry budget exhausted: " + reason, Audit: a}
	}
	a.Verdict, a.RejectionReason, a.Executed = v, reason, false
	return Decision{Verdict: v, Reason: reason, Audit: a}
}

// Fingerprint returns a stable content hash of v (canonical JSON -> sha256). It is the ONE
// canonical way to fingerprint an action/evidence: the kernel, the bridge, and the SDK all use
// it, so an integration binds an execution to an authorization by presenting the actual
// structured action, not by re-deriving a hash. Go's json.Marshal sorts map keys, so the
// encoding is independent of dictionary key order and deterministic for the shapes used here.
func Fingerprint(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
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
