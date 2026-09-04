// Package telemetry defines the canonical runtime data contract for an ARK run:
// one stable DecisionRecord per meaningful agent decision, and one RunResult per run.
// Aggregates are DERIVED from the decision records so there is a single source of truth.
//
// This is observability/telemetry only. It changes no runtime behavior, makes no
// cost-effectiveness judgments, and never serializes secrets. It is intended to become
// the foundation of the future ARK SDK. See CONTRACT.md.
package telemetry

import (
	"encoding/json"
	"fmt"
	"time"
)

// DecisionType classifies a decision. Free-form; these are the common ARK kinds.
type DecisionType string

const (
	DecisionToolCall    DecisionType = "tool_call"
	DecisionComplete    DecisionType = "complete"
	DecisionRetry       DecisionType = "retry"
	DecisionGrounding   DecisionType = "grounding"
	DecisionSupervision DecisionType = "supervision"
)

// Cost is FACTUAL per-decision cost attribution in US dollars. It describes what a
// decision cost — never whether it was "worth it". ModelCost + ToolCost == TotalCost.
type Cost struct {
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	InputCost    float64 `json:"input_cost"`
	OutputCost   float64 `json:"output_cost"`
	ModelCost    float64 `json:"model_cost"`          // input+output (model side)
	ToolCost     float64 `json:"tool_cost,omitempty"` // known tool API cost, if any
	TotalCost    float64 `json:"total_cost"`          // total attributable to this decision
}

// Verification captures a verification / self-correction signal, if one applied.
type Verification struct {
	Method     string   `json:"method,omitempty"` // e.g. "structural", "semantic"
	Passed     *bool    `json:"passed,omitempty"`
	Score      *float64 `json:"score,omitempty"`
	Confidence *float64 `json:"confidence,omitempty"`
	Issues     []string `json:"issues,omitempty"`
}

// Supervision captures a constrained-supervision intervention, if one applied. It mirrors
// the pkg/supervise verdict semantics; the mechanism never authors the action, so this
// records only the judgment + structured, safe REFERENCES to the action and evidence it
// evaluated (fingerprints/ids, never the raw evidence or secrets). An incident responder can
// reconstruct WHY an action was allowed/blocked from these fields.
type Supervision struct {
	ApplicableConstraint string `json:"applicable_constraint,omitempty"`
	Scope                string `json:"scope,omitempty"`          // resource/entity this verdict concerns
	TransactionID        string `json:"transaction_id,omitempty"` // one consequential authorization lifecycle
	Verdict              string `json:"verdict,omitempty"`        // ALLOW/REJECT/REQUIRE_EVIDENCE/RECOVERY_EXHAUSTED

	// what was proposed (structured, safe): the opaque option/kind, a fingerprint of the exact
	// proposed action, and a REDACTED view of its parameters (secret-like keys masked) so an
	// investigator can read the important parameters without leaking secrets.
	ProposedOption          string `json:"proposed_option,omitempty"`
	ProposedKind            string `json:"proposed_kind,omitempty"`
	ProposedFingerprint     string `json:"proposed_fingerprint,omitempty"`
	ProposedFieldsRedacted  string `json:"proposed_fields_redacted,omitempty"`

	// which evidence state produced the verdict: a caller id or content fingerprint, plus the
	// provenance/version/observed-at/expires-at the caller supplied. Never the raw evidence.
	TrustedEvidenceRef     string `json:"trusted_evidence_ref,omitempty"`
	EvidenceFingerprint    string `json:"evidence_fingerprint,omitempty"`
	EvidenceSource         string `json:"evidence_source,omitempty"`
	EvidenceVersion        string `json:"evidence_version,omitempty"`
	EvidenceObservedAtUnix int64  `json:"evidence_observed_at_unix,omitempty"`
	EvidenceExpiresAtUnix  int64  `json:"evidence_expires_at_unix,omitempty"`
	// trusted-evidence plane: which trust channel the facts came through, and (for a provider)
	// which provider, which subject/entity, and the request binding — so an auditor can answer
	// "which trusted provider supplied the facts that allowed this action?".
	EvidenceTrust        string `json:"evidence_trust,omitempty"`
	EvidenceProviderID   string `json:"evidence_provider_id,omitempty"`
	EvidenceSubject      string `json:"evidence_subject,omitempty"`
	EvidenceRequestFP    string `json:"evidence_request_fingerprint,omitempty"`

	RejectionReason       string `json:"rejection_reason,omitempty"`
	RetryNumber           *int   `json:"retry_number,omitempty"`
	SuggestedFromEvidence string `json:"suggested_from_evidence,omitempty"`
	Executed              *bool  `json:"executed,omitempty"`

	// Authorization lifecycle (ISSUED -> CONSUMED -> COMPLETED), each stamped at its real time.
	// IdempotencyKey is a stable id for THIS authorization the integration may forward to a
	// cooperating external API to dedupe the real side effect (ARK does not own that side effect).
	AuthState      string `json:"auth_state,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	IssuedAtUnix   int64  `json:"issued_at_unix,omitempty"`
	ConsumedAtUnix int64  `json:"consumed_at_unix,omitempty"`
	ExecutedAtUnix int64  `json:"executed_at_unix,omitempty"`
}

// DecisionRecord is one meaningful runtime decision. Fields that do not apply are left
// nil/zero rather than fabricated. It describes what happened — no interpretation.
type DecisionRecord struct {
	ID        string    `json:"id"` // stable: decision_001, decision_002, ...
	Sequence  int       `json:"sequence"`
	Timestamp time.Time `json:"timestamp,omitempty"`

	DecisionType DecisionType `json:"decision_type"`
	Action       string       `json:"action,omitempty"`

	// AgentID is a generic identifier for the logical agent that made this proposal. It is
	// audit metadata only — never an authorization authority by itself — and lets telemetry
	// distinguish Agent A / B / C in a multi-agent run.
	AgentID string `json:"agent_id,omitempty"`

	Model         string `json:"model,omitempty"`
	RoutingReason string `json:"routing_reason,omitempty"`

	Tool        string `json:"tool,omitempty"`
	ToolArgsRef string `json:"tool_args_ref,omitempty"` // redacted/reference — never raw secrets

	Cost      Cost  `json:"cost"`
	LatencyMs int64 `json:"latency_ms"`

	Verification *Verification `json:"verification,omitempty"`
	Supervision  *Supervision  `json:"supervision,omitempty"`

	Outcome  string `json:"outcome,omitempty"`
	Error    string `json:"error,omitempty"`
	Executed bool   `json:"executed"`
}

// ---- run-level summaries (all derived from decisions) ----

type SupervisionSummary struct {
	Enabled       bool           `json:"enabled"`
	Interventions int            `json:"interventions"`
	ByVerdict     map[string]int `json:"by_verdict,omitempty"`
}

type RoutingSummary struct {
	ByModel map[string]int `json:"by_model"`
}

type ToolSummary struct {
	Calls  int            `json:"calls"`
	ByTool map[string]int `json:"by_tool,omitempty"`
}

// RunResult is the canonical structured result of one ARK run. It represents THIS RUN ONLY.
// Historical/learned state (governor / model-capability registry) is deliberately NOT here.
type RunResult struct {
	RunID       string    `json:"run_id"`
	Task        string    `json:"task"`
	TaskType    string    `json:"task_type,omitempty"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	CompletedAt time.Time `json:"completed_at,omitempty"`

	Success           bool   `json:"success"`
	TerminationReason string `json:"termination_reason,omitempty"`
	Output            string `json:"output,omitempty"`

	Decisions []DecisionRecord `json:"decisions"`

	TotalTokens    int     `json:"total_tokens"`
	TotalLatencyMs int64   `json:"total_latency_ms"`
	TotalCost      float64 `json:"total_cost"`

	// Cost views, all derived from Decisions. by_model and by_action each partition the
	// total; by_tool covers only tool-associated decisions; by_supervision groups the cost
	// of decisions carrying a supervision verdict (factual, no judgment).
	CostByModel       map[string]float64 `json:"cost_by_model"`
	CostByTool        map[string]float64 `json:"cost_by_tool"`
	CostByAction      map[string]float64 `json:"cost_by_action"`
	CostBySupervision map[string]float64 `json:"cost_by_supervision,omitempty"`

	Supervision SupervisionSummary `json:"supervision"`
	Routing     RoutingSummary     `json:"routing"`
	Tools       ToolSummary        `json:"tools"`

	// Providers reports configured/absent status ONLY — never secrets.
	Providers map[string]string `json:"providers,omitempty"`

	Errors []string `json:"errors,omitempty"`
}

// ---- Builder: assemble decisions, derive the run ----

// Builder accumulates DecisionRecords and derives the RunResult. Callers add one decision
// per meaningful runtime decision; aggregates are computed once, in Finish.
type Builder struct {
	run RunResult
	seq int
}

// NewRun starts a run. clock defaults to time.Now when zero.
func NewRun(runID, task string) *Builder {
	return &Builder{run: RunResult{
		RunID:     runID,
		Task:      task,
		StartedAt: time.Now().UTC(),
		Providers: map[string]string{},
	}}
}

func (b *Builder) SetTaskType(t string) *Builder      { b.run.TaskType = t; return b }
func (b *Builder) SetStartedAt(t time.Time) *Builder  { b.run.StartedAt = t; return b }
func (b *Builder) SetProvider(name, status string) *Builder {
	b.run.Providers[name] = status // status must be "configured"/"absent" — never a secret
	return b
}

// AddDecision appends a decision, assigning the stable ID (decision_NNN) and sequence.
// Returns the assigned ID so callers can cross-reference (e.g. a supervised retry chain).
func (b *Builder) AddDecision(d DecisionRecord) string {
	b.seq++
	d.Sequence = b.seq
	d.ID = fmt.Sprintf("decision_%03d", b.seq)
	if d.Timestamp.IsZero() {
		d.Timestamp = time.Now().UTC()
	}
	// keep TotalCost consistent with its parts (no fabrication if caller already set it)
	if d.Cost.TotalCost == 0 && (d.Cost.ModelCost != 0 || d.Cost.ToolCost != 0) {
		d.Cost.TotalCost = d.Cost.ModelCost + d.Cost.ToolCost
	}
	b.run.Decisions = append(b.run.Decisions, d)
	return d.ID
}

// Finish derives all aggregates from the decisions (single source of truth) and returns the
// RunResult. It never reads historical/governor state.
func (b *Builder) Finish(success bool, terminationReason, output string) RunResult {
	r := &b.run
	r.Success = success
	r.TerminationReason = terminationReason
	r.Output = output
	r.CompletedAt = time.Now().UTC()

	r.CostByModel = map[string]float64{}
	r.CostByTool = map[string]float64{}
	r.CostByAction = map[string]float64{}
	r.CostBySupervision = map[string]float64{}
	r.Routing = RoutingSummary{ByModel: map[string]int{}}
	r.Tools = ToolSummary{ByTool: map[string]int{}}
	r.Supervision = SupervisionSummary{ByVerdict: map[string]int{}}

	for i := range r.Decisions {
		d := &r.Decisions[i]
		r.TotalTokens += d.Cost.InputTokens + d.Cost.OutputTokens
		r.TotalLatencyMs += d.LatencyMs
		r.TotalCost += d.Cost.TotalCost

		if d.Model != "" {
			r.CostByModel[d.Model] += d.Cost.TotalCost
			r.Routing.ByModel[d.Model]++
		}
		if d.Tool != "" {
			r.CostByTool[d.Tool] += d.Cost.TotalCost
			r.Tools.Calls++
			r.Tools.ByTool[d.Tool]++
		}
		actionKey := string(d.DecisionType)
		if d.Action != "" {
			actionKey = d.Action
		}
		r.CostByAction[actionKey] += d.Cost.TotalCost

		if d.Supervision != nil {
			r.Supervision.Enabled = true
			if v := d.Supervision.Verdict; v != "" {
				// an intervention is a non-ALLOW verdict; ALLOW is the pass-through
				if v != "ALLOW" {
					r.Supervision.Interventions++
				}
				r.Supervision.ByVerdict[v]++
				r.CostBySupervision[v] += d.Cost.TotalCost
			}
		}
		if d.Error != "" {
			r.Errors = append(r.Errors, d.Error)
		}
	}
	if len(r.CostBySupervision) == 0 {
		r.CostBySupervision = nil
	}
	if len(r.Supervision.ByVerdict) == 0 {
		r.Supervision.ByVerdict = nil
	}
	return *r
}

// ---- JSON (the smallest way to emit/read the structured run result) ----

// JSON returns the canonical JSON encoding of the run result.
func (r RunResult) JSON() ([]byte, error) { return json.MarshalIndent(r, "", "  ") }

// ParseRunResult decodes a RunResult from its canonical JSON.
func ParseRunResult(b []byte) (RunResult, error) {
	var r RunResult
	err := json.Unmarshal(b, &r)
	return r, err
}

// ---- security / redaction helpers ----

// secretKey reports whether a tool-arg key name looks like it holds a secret.
func secretKey(k string) bool {
	lk := toLower(k)
	for _, s := range []string{"token", "secret", "password", "passwd", "api_key", "apikey",
		"authorization", "auth", "key", "credential", "bearer"} {
		if contains(lk, s) {
			return true
		}
	}
	return false
}

// RedactToolArgs returns a redaction-safe reference to tool arguments: secret-looking
// fields are replaced with "***". The contract never requires raw secrets to be logged.
func RedactToolArgs(args map[string]any) map[string]any {
	if args == nil {
		return nil
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		if secretKey(k) {
			out[k] = "***"
			continue
		}
		out[k] = v
	}
	return out
}

// ProviderStatus maps a boolean "is a credential configured" to a secret-free status
// string. It never accepts or emits the credential itself.
func ProviderStatus(configured bool) string {
	if configured {
		return "configured"
	}
	return "absent"
}

// tiny stdlib-free helpers (avoid importing strings for two calls)
func toLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}
func contains(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
