package main

// session.go implements the persistent external-agent session (`ark-bridge --session`).
//
// The developer keeps their own agent/model/tools and drives their own loop; they REPORT
// what happened and ask ARK to gate proposed actions. This side is deliberately the
// AUTHORITY on everything ARK owns, so the stateless Python client never reimplements it:
//
//   - retry counters per constraint            -> extSession.retry      (Go)
//   - verdict semantics + RECOVERY_EXHAUSTED    -> pkg/supervise         (unchanged)
//   - stable decision IDs + the audit chain     -> extSession.seq/byID   (Go)
//   - cost from reported tokens+model           -> pkg/cost.ModelPricing (unchanged)
//   - RunResult assembly + all aggregates       -> pkg/telemetry.Builder (unchanged)
//
// Provenance is tracked explicitly: for every decision we distinguish facts the external
// runtime REPORTED from facts ARK DERIVED, so nothing developer-supplied is presented as if
// ARK observed it directly.
//
// Protocol: one compact JSON object per line on stdin -> one compact JSON object per line on
// stdout. Commands: start, check, record, finish. finish ends the loop.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/atripati/ark/pkg/authz"
	"github.com/atripati/ark/pkg/cost"
	"github.com/atripati/ark/pkg/supervise"
	"github.com/atripati/ark/pkg/telemetry"
)

// clockSkewSec bounds how far into the future an evidence observed_at may be before ARK refuses
// to treat it as fresh (defends against fabricated far-future timestamps + real clock skew).
const clockSkewSec = 60

type sessionCmd struct {
	Cmd string `json:"cmd"`

	// start
	RunID       string `json:"run_id"`
	Task        string `json:"task"`
	TaskType    string `json:"task_type"`
	Supervision string `json:"supervision"` // "off" (default) | "experimental"
	Provider    string `json:"provider"`    // used for cost pricing of reported models
	Budget      int    `json:"budget"`

	// check (supervision gate — reported proposal + trusted evidence)
	Action        string                   `json:"action"`
	Tool          string                   `json:"tool"`
	Constraint    string                   `json:"constraint"`
	Scope         string                   `json:"scope"`          // resource/entity affected (audit)
	Namespace     string                   `json:"namespace"`      // tenant/application boundary (multi-tenant isolation; default "")
	TransactionID string                   `json:"transaction_id"` // one authorization lifecycle; retry is isolated by this
	AgentID       string                   `json:"agent_id"`       // logical agent making the proposal (audit metadata)
	Proposed      supervise.ProposedAction `json:"proposed"`
	// Evidence stays raw so it can be decoded STRICTLY (unknown/typo'd fields rejected) rather
	// than silently coerced to Go zero values that could produce ALLOW.
	Evidence          json.RawMessage `json:"evidence"`
	MaxEvidenceAgeSec int64           `json:"max_evidence_age_sec"`     // 0 = no freshness requirement
	RequireTrustedProvider bool       `json:"require_trusted_provider"` // trusted integration demands provider-resolved evidence

	// record (reported execution telemetry)
	Model         string          `json:"model"`
	ToolArgs      map[string]any  `json:"tool_args"`
	InputTokens   int             `json:"input_tokens"`
	OutputTokens  int             `json:"output_tokens"`
	Cost          *float64        `json:"cost"` // nil -> ARK derives from tokens+model
	LatencyMs     int64           `json:"latency_ms"`
	RoutingReason string          `json:"routing_reason"`
	Verification  *verificationIn `json:"verification"`
	Outcome       string          `json:"outcome"`
	Error         string          `json:"error"` // populates the canonical DecisionRecord.error
	Executed        *bool  `json:"executed"`
	Of              string `json:"of"`               // decision_NNN this telemetry completes (in-session)
	AuthorizationID string `json:"authorization_id"` // stable id, for consume/record across restart/instances
	// ExecutedAction is the actual structured action executed. For record(of=<ALLOW>,
	// executed=true) it is MANDATORY: ARK canonicalizes it and requires its fingerprint to match
	// the action that received ALLOW. nil (absent) when confirming execution -> fail closed.
	ExecutedAction *supervise.ProposedAction `json:"executed_action"`

	// finish
	Success           bool   `json:"success"`
	TerminationReason string `json:"termination_reason"`
	Output            string `json:"output"`
}

type verificationIn struct {
	Method     string   `json:"method"`
	Passed     *bool    `json:"passed"`
	Score      *float64 `json:"score"`
	Confidence *float64 `json:"confidence"`
	Issues     []string `json:"issues"`
}

// decisionProv records, per decision, which facts came from the external runtime vs ARK.
type decisionProv struct {
	Reported []string `json:"reported"`
	Derived  []string `json:"derived"`
}

type extSession struct {
	// mu guards ALL mutable session state below. The session must be self-safe for concurrent
	// callers rather than relying on the transport happening to serialize — that would make the
	// kernel's safety depend on one client implementation (INV-09).
	mu sync.Mutex

	runID       string
	task        string
	taskType    string
	provider    string
	supervision string
	budget      int
	sup         *supervise.Supervisor

	decisions []*telemetry.DecisionRecord // creation order (== Builder add order at finish)
	byID      map[string]*telemetry.DecisionRecord
	prov      map[string]*decisionProv
	// store is the durable authorization + retry seam (DUR-*). Default: in-memory (state lost on
	// termination, old authorizations fail closed after restart). Durable: a filesystem store
	// (ARK_AUTHZ_DIR) whose ISSUED->CONSUMED transition is atomic + single-winner across processes.
	store    authz.Store
	storeErr error // a durable store that failed to open -> supervision fails CLOSED (DUR-05)
	// authByDecision maps an in-session decision_id -> stable authorization_id, so callers may
	// reference an authorization by the verdict they hold (of=) within a live session.
	authByDecision map[string]string
	providers      map[string]bool // providers the external runtime reported using
	seq            int
}

func newExtSession(c sessionCmd) *extSession {
	runID := c.RunID
	if runID == "" {
		runID = fmt.Sprintf("ext-run-%d", time.Now().UnixNano())
	}
	prov := c.Provider
	if prov == "" {
		prov = "openai"
	}
	budget := c.Budget
	if budget == 0 {
		budget = 4
	}
	sv := c.Supervision
	if sv == "" {
		sv = "off"
	}
	s := &extSession{
		runID: runID, task: c.Task, taskType: c.TaskType, provider: prov,
		supervision: sv, budget: budget, sup: supervise.New(),
		byID: map[string]*telemetry.DecisionRecord{}, prov: map[string]*decisionProv{},
		authByDecision: map[string]string{}, providers: map[string]bool{},
	}
	// Durable mode is opt-in via ARK_AUTHZ_DIR. If the durable store cannot be opened, we do NOT
	// silently fall back to in-memory for a session that asked for durability — we remember the
	// error and fail supervision closed (DUR-05). Default (unset) is the in-memory reference store.
	if dir := os.Getenv("ARK_AUTHZ_DIR"); dir != "" {
		fs, err := authz.OpenFileStore(dir)
		if err != nil {
			s.storeErr = err
		} else {
			s.store = fs
		}
	} else {
		s.store = authz.NewMemStore()
	}
	return s
}

// check evaluates a reported proposal against a runtime constraint. ARK owns the retry
// counter + verdict; it never authors the replacement action. It FAILS CLOSED: an unknown
// constraint, a missing scope, or malformed evidence are refused as errors — never silently
// turned into ALLOW.
func (s *extSession) check(c sessionCmd) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.supervision != "experimental" {
		return errReply("constrained supervision is experimental and off by default; open the trace with supervision='experimental'")
	}
	// Scope names the resource/entity affected. It must be present for audit + the default
	// transaction identity.
	if strings.TrimSpace(c.Scope) == "" {
		return errReply("supervision check requires a non-empty 'scope' identifying the resource/entity being supervised")
	}
	// Unknown constraint: fail closed. ARK will not authorize an action under a constraint it
	// does not implement (INV-01). Surfacing this as an error (not a verdict) prevents it from
	// being mistaken for ALLOW or "recovered" from by the agent loop.
	if !s.sup.Has(c.Constraint) {
		return errReply("unknown constraint '" + c.Constraint + "': ARK fails closed and will not authorize an action under a constraint it does not implement; register the constraint or correct the name")
	}
	// Strict evidence decode + validation: typo'd/unknown fields and structurally invalid
	// evidence are refused rather than coerced to zero values that could produce ALLOW.
	ev, verr := decodeEvidenceStrict(s.sup, c.Evidence, c.Constraint)
	if verr != nil {
		return errReply("malformed evidence: " + verr.Error())
	}

	// transaction identity: retry state is isolated per (transaction, constraint). Two distinct
	// transactions for the same entity therefore never share a budget (INV-07/DUR-07). Defaults to
	// scope when the caller does not distinguish transactions.
	txn := strings.TrimSpace(c.TransactionID)
	if txn == "" {
		txn = c.Scope
	}

	// A session that requested a durable store but could not open it FAILS CLOSED (DUR-05).
	if s.storeErr != nil {
		return errReply("supervision store unavailable (fail closed): " + s.storeErr.Error())
	}

	now := time.Now().UTC() // the actual verdict time (D3: stamped now, not at finish)
	key := retryKey(txn, c.Constraint)
	rc, rerr := s.store.RetryCount(key) // durable retry state — survives restart (DUR/Phase 6)
	if rerr != nil {
		return errReply("supervision store unavailable reading retry state (fail closed): " + rerr.Error())
	}
	d, err := s.sup.Evaluate(supervise.Request{
		Constraint: c.Constraint, Namespace: c.Namespace, Transaction: txn,
		Scope: c.Scope, Proposed: c.Proposed, Evidence: ev,
		RetryCount: rc, Budget: s.budget,
		NowUnix:                now.Unix(),
		Freshness:              supervise.FreshnessPolicy{MaxAgeSec: c.MaxEvidenceAgeSec, SkewSec: clockSkewSec},
		RequireTrustedProvider: c.RequireTrustedProvider,
	})
	if err != nil {
		return errReply("supervision refused (fail closed): " + err.Error())
	}
	if d.Verdict != supervise.Allow {
		if _, ierr := s.store.IncrRetry(key); ierr != nil {
			return errReply("supervision store unavailable recording retry (fail closed): " + ierr.Error())
		}
	}

	s.seq++
	id := fmt.Sprintf("decision_%03d", s.seq)
	notExec := false
	evRef := ev.Meta.EvidenceID
	if evRef == "" {
		evRef = d.Audit.EvidenceFingerprint
	}
	// stable, content-derived authorization id (== idempotency key): survives restart, tied to the
	// FULL security namespace (namespace/tenant, transaction, scope, constraint, action fp,
	// evidence fp) so it can never alias another tenant/scope/policy/action/evidence (DUR-03/06/09).
	authID := authz.ID(c.Namespace, txn, c.Scope, c.Constraint, d.Audit.ProposedFingerprint, d.Audit.EvidenceFingerprint)

	// On ALLOW, durably ISSUE the authorization. A store failure here FAILS CLOSED — ARK never
	// returns an ALLOW it could not persist (DUR-05/11).
	if d.Verdict == supervise.Allow {
		if crErr := s.store.Create(authz.Record{
			ID: authID, Namespace: c.Namespace, RunID: s.runID, DecisionID: id, Transaction: txn, Scope: c.Scope,
			AgentID: c.AgentID, Constraint: c.Constraint,
			ActionFP: d.Audit.ProposedFingerprint, EvidenceFP: d.Audit.EvidenceFingerprint,
			ObservedAt: ev.Meta.ObservedAtUnix, ExpiresAt: ev.Meta.ExpiresAtUnix,
			MaxAgeSec: c.MaxEvidenceAgeSec, IssuedAt: now.Unix(),
		}); crErr != nil {
			return errReply("supervision store could not persist the authorization (fail closed): " + crErr.Error())
		}
		s.authByDecision[id] = authID
	}

	sup := telemetry.SupervisionFromDecision(d, evRef)
	sup.Executed = &notExec // not executed yet; consume/record flips this on ALLOW+execute
	sup.TransactionID = txn
	sup.EvidenceExpiresAtUnix = ev.Meta.ExpiresAtUnix
	sup.ProposedFieldsRedacted = redactedFields(c.Proposed.Fields)
	// trusted-evidence provenance (Phase 14): which channel/provider/subject established the facts.
	sup.EvidenceTrust = string(ev.Meta.Trust)
	sup.EvidenceProviderID = ev.Meta.ProviderID
	sup.EvidenceSubject = ev.Meta.Subject
	sup.EvidenceRequestFP = ev.Meta.RequestFingerprint
	if d.Verdict == supervise.Allow {
		sup.AuthState = string(authz.Issued)
		sup.IdempotencyKey = authID
		sup.IssuedAtUnix = now.Unix()
	}
	rec := &telemetry.DecisionRecord{
		ID: id, Sequence: s.seq, DecisionType: mapDecisionType(c.Action),
		Action: c.Action, Tool: c.Tool, AgentID: c.AgentID, Supervision: sup, Executed: false,
		Timestamp: now, // D3: the verdict's actual time, preserved through finish()
	}
	s.decisions = append(s.decisions, rec)
	s.byID[id] = rec
	s.prov[id] = &decisionProv{
		Reported: []string{"action", "proposed", "constraint", "scope", "transaction_id", "agent_id", "evidence"},
		Derived: []string{"id", "sequence", "timestamp", "supervision.verdict",
			"supervision.retry_number", "supervision.suggested_from_evidence",
			"supervision.proposed_fingerprint", "supervision.evidence_fingerprint",
			"supervision.idempotency_key", "supervision.issued_at_unix"},
	}
	s.auditEvent("issued", rec)
	return map[string]any{
		"ok": true, "decision_id": id,
		"verdict": string(d.Verdict), "reason": d.Reason,
		"retry_number": d.Audit.RetryNumber, "suggested": d.Audit.SuggestedFromEvidence,
		"allowed":              d.Verdict == supervise.Allow,
		"scope":                c.Scope,
		"transaction_id":       txn,
		"action_fingerprint":   d.Audit.ProposedFingerprint,
		"evidence_fingerprint": d.Audit.EvidenceFingerprint,
		"authorization_id":     authID,
		"idempotency_key":      authID,
	}
}

// resolveAuthID maps a consume/record command to the stable authorization id: an explicit
// authorization_id (works across restart/instances) takes precedence, else the in-session
// decision id (of=).
func (s *extSession) resolveAuthID(c sessionCmd) string {
	if strings.TrimSpace(c.AuthorizationID) != "" {
		return c.AuthorizationID
	}
	if c.Of != "" {
		return s.authByDecision[c.Of] // "" if not an in-session ALLOW
	}
	return ""
}

// auditEvent best-effort appends a durable audit line if the store is an AuditSink (Phase 9).
func (s *extSession) auditEvent(event string, rec *telemetry.DecisionRecord) {
	sink, ok := s.store.(authz.AuditSink)
	if !ok || rec == nil || rec.Supervision == nil {
		return
	}
	sv := rec.Supervision
	_ = sink.AppendAudit(map[string]any{
		"event": event, "run_id": s.runID, "decision_id": rec.ID, "agent_id": rec.AgentID,
		"transaction_id": sv.TransactionID, "scope": sv.Scope, "tool": rec.Tool,
		"authorization_id": sv.IdempotencyKey, "auth_state": sv.AuthState, "verdict": sv.Verdict,
		"constraint": sv.ApplicableConstraint, "action_fingerprint": sv.ProposedFingerprint,
		"proposed_params_redacted": sv.ProposedFieldsRedacted,
		"evidence_trust":           sv.EvidenceTrust, "evidence_provider_id": sv.EvidenceProviderID,
		"evidence_subject":         sv.EvidenceSubject, "evidence_request_fingerprint": sv.EvidenceRequestFP,
		"evidence_ref":             sv.TrustedEvidenceRef, "evidence_fingerprint": sv.EvidenceFingerprint,
		"issued_at": sv.IssuedAtUnix, "consumed_at": sv.ConsumedAtUnix, "executed_at": sv.ExecutedAtUnix,
	})
}

// retryKey binds retry state to the exact (transaction, constraint). The unit separator can
// never appear inside a JSON string field unescaped, so the key is unambiguous.
func retryKey(transaction, constraint string) string { return transaction + "\x1f" + constraint }

// authStoreErr maps a store error into a clear, fail-closed message. EVERY branch is a refusal —
// unknown, malformed, corrupt, incompatible schema, and unavailable all deny; none clear.
func authStoreErr(op, authID string, err error) string {
	switch {
	case errors.Is(err, authz.ErrNotFound):
		return op + ": unknown authorization " + authID
	case errors.Is(err, authz.ErrBadID):
		return op + ": malformed authorization id"
	case errors.Is(err, authz.ErrCorrupt):
		return op + ": authorization state is CORRUPT and needs reconciliation for " + authID
	case errors.Is(err, authz.ErrSchema):
		return op + ": incompatible durable schema"
	default:
		return op + ": authorization store unavailable: " + err.Error()
	}
}

// redactedFields returns the proposed action's parameters with secret-like keys masked, as a
// compact JSON string, so the audit preserves readable non-secret parameters (Phase 7).
func redactedFields(f map[string]any) string {
	if len(f) == 0 {
		return ""
	}
	b, err := json.Marshal(telemetry.RedactToolArgs(f))
	if err != nil {
		return ""
	}
	return string(b)
}

// decodeEvidenceStrict decodes a raw evidence payload with unknown fields REJECTED (so a
// typo like `requestedRank` is a loud error, not a silent rank=0), then applies semantic
// validation. Empty/absent evidence is refused: supervision requires trusted evidence.
func decodeEvidenceStrict(sup *supervise.Supervisor, raw json.RawMessage, constraint string) (supervise.Evidence, error) {
	var e supervise.Evidence
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return e, fmt.Errorf("no evidence provided; supervision requires trusted runtime evidence")
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&e); err != nil {
		return e, err
	}
	if err := sup.ValidateEvidence(constraint, e); err != nil {
		return e, err
	}
	return e, nil
}

// consume is the PRE-EXECUTION gate (Phase 1). The integration calls it IMMEDIATELY BEFORE the
// real side effect. It re-validates the authorization against a fresh clock and consumes it
// exactly once, so a stale, replayed, action-mismatched, or non-ALLOW authorization can never
// reach the side effect (INV-03/04/06/08). Execution may proceed ONLY when cleared=true.
func (s *extSession) consume(c sessionCmd) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.storeErr != nil {
		return errReply("supervision store unavailable (fail closed): " + s.storeErr.Error())
	}
	authID := s.resolveAuthID(c)
	if authID == "" {
		return errReply("consume requires 'authorization_id' (or an in-session 'of' for an ALLOW decision)")
	}
	// Read the IMMUTABLE authorization identity. Only ALLOW authorizations exist in the store, so
	// a non-ALLOW or unknown reference is ErrNotFound -> refused (INV-03/DUR-04). A store failure
	// is ErrStore -> fail closed, never treated as "proceed" (DUR-05/12).
	a, gerr := s.store.Get(authID)
	if gerr != nil {
		return errReply("consume refused (fail closed): " + authStoreErr("consume", authID, gerr))
	}
	if a.State == authz.Consumed || a.State == authz.Completed {
		return errReply(fmt.Sprintf("consume: authorization %s was already consumed (state=%s); refusing replay", authID, a.State))
	}
	if c.ExecutedAction == nil {
		return errReply(fmt.Sprintf("consume: authorization %s requires 'executed_action' (the action about to execute) to bind it", authID))
	}
	if supervise.Fingerprint(*c.ExecutedAction) != a.ActionFP { // DUR-08
		return errReply(fmt.Sprintf("consume: the action about to execute does not match the authorized action for %s; this authorization does not cover it", authID))
	}
	if strings.TrimSpace(c.TransactionID) != "" && c.TransactionID != a.Transaction { // DUR-07
		return errReply(fmt.Sprintf("consume: transaction %q does not own authorization %s (owned by %q)", c.TransactionID, authID, a.Transaction))
	}
	now := time.Now().UTC()
	// Freshness re-check at USE time (TOCTOU, INV-06): if a window was configured at issue, it
	// must STILL hold now — before the side effect, not merely detected afterward.
	if a.MaxAgeSec > 0 || a.ExpiresAt > 0 {
		if fresh, why := supervise.CheckFreshness(a.ObservedAt, a.ExpiresAt, now.Unix(), a.MaxAgeSec, clockSkewSec); !fresh {
			// explicit NON-EXECUTABLE result — NOT cleared, and NOT consumed. Do not execute.
			return map[string]any{"ok": true, "cleared": false, "requires_recheck": true,
				"authorization_id": authID, "decision_id": a.DecisionID,
				"reason": "authorization is stale at execution time: " + why + "; re-check with fresh evidence before executing"}
		}
	}
	// ATOMIC single-winner transition ISSUED -> CONSUMED (durable; safe across processes).
	consumed, cerr := s.store.Consume(authID, now.Unix())
	if cerr != nil {
		if errors.Is(cerr, authz.ErrAlreadyConsumed) {
			return errReply(fmt.Sprintf("consume: authorization %s was already consumed; refusing replay", authID))
		}
		return errReply("consume: authorization store unavailable (fail closed): " + cerr.Error())
	}
	if rec := s.byID[consumed.DecisionID]; rec != nil && rec.Supervision != nil {
		rec.Supervision.AuthState = string(authz.Consumed)
		rec.Supervision.ConsumedAtUnix = now.Unix()
		s.auditEvent("consumed", rec)
	}
	return map[string]any{"ok": true, "cleared": true, "authorization_id": authID,
		"decision_id": consumed.DecisionID, "idempotency_key": authID, "consumed_at_unix": now.Unix()}
}

// status reports the durable lifecycle state of an authorization, so an operator (or the
// integration) can RECONCILE after a crash. A CONSUMED state means "claimed for execution; the
// external outcome may not be known" — explicitly ambiguous, never silently retried (Phase 13).
func (s *extSession) status(c sessionCmd) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.storeErr != nil {
		return errReply("supervision store unavailable (fail closed): " + s.storeErr.Error())
	}
	authID := s.resolveAuthID(c)
	if authID == "" {
		return errReply("status requires 'authorization_id' (or an in-session 'of')")
	}
	a, err := s.store.Get(authID)
	if err != nil {
		if errors.Is(err, authz.ErrNotFound) {
			return map[string]any{"ok": true, "authorization_id": authID, "state": "UNKNOWN",
				"reconcile": "no such authorization in this store (lost, or a different store)"}
		}
		if errors.Is(err, authz.ErrCorrupt) {
			return map[string]any{"ok": true, "authorization_id": authID, "state": "CORRUPT",
				"reconcile": "on-disk authorization state is inconsistent/damaged; do NOT execute; reconcile against the target system using the idempotency key before any action"}
		}
		return errReply("status refused (fail closed): " + authStoreErr("status", authID, err))
	}
	reconcile := ""
	if a.State == authz.Consumed {
		reconcile = "AMBIGUOUS: authorization was claimed but no completion was recorded; the external side effect may or may not have occurred — reconcile against the target system using the idempotency key before any retry"
	}
	return map[string]any{"ok": true, "authorization_id": authID, "state": string(a.State),
		"transaction_id": a.Transaction, "issued_at_unix": a.IssuedAt,
		"consumed_at_unix": a.ConsumedAt, "completed_at_unix": a.CompletedAt,
		"idempotency_key": authID, "reconcile": reconcile}
}

// record attaches reported execution telemetry, either to a prior check (of=decision_NNN)
// or as a fresh decision. ARK derives only IDs/ordering/cost — never the model/tool facts.
func (s *extSession) record(c sessionCmd) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	var rec *telemetry.DecisionRecord
	if c.Of != "" {
		rec = s.byID[c.Of]
		if rec == nil {
			return errReply("record: unknown of=" + c.Of)
		}
	} else {
		s.seq++
		id := fmt.Sprintf("decision_%03d", s.seq)
		// D3: a fresh decision is stamped at its actual creation time (preserved through finish).
		rec = &telemetry.DecisionRecord{ID: id, Sequence: s.seq, Timestamp: time.Now().UTC()}
		s.decisions = append(s.decisions, rec)
		s.byID[id] = rec
		s.prov[id] = &decisionProv{}
	}
	p := s.prov[rec.ID]
	rep := func(f string) { p.Reported = append(p.Reported, f) }
	der := func(f string) { p.Derived = append(p.Derived, f) }

	if c.Action != "" {
		rec.Action = c.Action
		rec.DecisionType = mapDecisionType(c.Action)
		rep("action")
	} else if rec.DecisionType == "" {
		rec.DecisionType = telemetry.DecisionComplete
	}
	if c.Model != "" {
		rec.Model = c.Model
		s.providers[s.provider] = true
		rep("model")
	}
	if c.Tool != "" {
		rec.Tool = c.Tool
		rep("tool")
	}
	if len(c.ToolArgs) > 0 {
		if b, err := json.Marshal(telemetry.RedactToolArgs(c.ToolArgs)); err == nil {
			rec.ToolArgsRef = string(b) // redacted reference — never raw secrets
		}
		rep("tool_args")
		der("tool_args_ref(redacted)")
	}
	if c.InputTokens != 0 || c.OutputTokens != 0 {
		rec.Cost.InputTokens = c.InputTokens
		rec.Cost.OutputTokens = c.OutputTokens
		rep("input_tokens")
		rep("output_tokens")
	}
	switch {
	case c.Cost != nil: // developer supplied a cost -> take it, do not fabricate
		rec.Cost.TotalCost = *c.Cost
		rec.Cost.ModelCost = *c.Cost
		rep("cost")
	case rec.Model != "" && (rec.Cost.InputTokens != 0 || rec.Cost.OutputTokens != 0):
		// ARK derives cost from reported tokens+model via the SAME pricing table the
		// runtime uses (pkg/cost). This is derived, not observed.
		price := cost.ModelPricing(s.provider, rec.Model)
		rec.Cost.InputCost = float64(rec.Cost.InputTokens) * price.InputPerToken
		rec.Cost.OutputCost = float64(rec.Cost.OutputTokens) * price.OutputPerToken
		rec.Cost.ModelCost = rec.Cost.InputCost + rec.Cost.OutputCost
		rec.Cost.TotalCost = rec.Cost.ModelCost
		der("cost")
	}
	if c.LatencyMs != 0 {
		rec.LatencyMs = c.LatencyMs
		rep("latency_ms")
	}
	if c.RoutingReason != "" {
		rec.RoutingReason = c.RoutingReason
		rep("routing_reason")
	}
	if c.Verification != nil {
		rec.Verification = &telemetry.Verification{
			Method: c.Verification.Method, Passed: c.Verification.Passed,
			Score: c.Verification.Score, Confidence: c.Verification.Confidence,
			Issues: c.Verification.Issues,
		}
		rep("verification")
	}
	if c.Outcome != "" {
		rec.Outcome = c.Outcome
		rep("outcome")
	}
	// A real failure populates the dedicated canonical error field (aggregated into
	// RunResult.errors by the Builder). Outcome is preserved separately for compatibility.
	if c.Error != "" {
		rec.Error = c.Error
		rep("error")
	}
	executed := true
	if c.Executed != nil {
		executed = *c.Executed
	}

	// Fail-closed execution guards for a decision that came from a supervision check.
	if executed {
		// INV-03: an in-session non-ALLOW verdict must never be recorded as executed, even without
		// a durable authorization (non-ALLOW decisions are not stored).
		if c.Of != "" {
			if r0 := s.byID[c.Of]; r0 != nil && r0.Supervision != nil &&
				r0.Supervision.Verdict != "" && r0.Supervision.Verdict != string(supervise.Allow) {
				return errReply(fmt.Sprintf(
					"refusing to record execution of decision %s: its verdict was %q, and a non-ALLOW action must never execute",
					c.Of, r0.Supervision.Verdict))
			}
		}
		// ALLOW authorization lifecycle, driven by the durable store.
		if authID := s.resolveAuthID(c); authID != "" {
			if s.storeErr != nil {
				return errReply("supervision store unavailable (fail closed): " + s.storeErr.Error())
			}
			a, gerr := s.store.Get(authID)
			if gerr != nil {
				return errReply("record refused (fail closed): " + authStoreErr("record", authID, gerr))
			}
			// INV-04 (MANDATORY action binding): present the actual executed action; a missing or
			// different action is refused. A bare fingerprint is not accepted.
			if c.ExecutedAction == nil {
				return errReply(fmt.Sprintf(
					"recording execution of authorization %s requires 'executed_action' (the actual action executed)", authID))
			}
			if execFP := supervise.Fingerprint(*c.ExecutedAction); execFP != a.ActionFP {
				return errReply(fmt.Sprintf(
					"executed action does not match the action ARK authorized for %s (authorized %s, executed %s)", authID, a.ActionFP, execFP))
			}
			now := time.Now().UTC()
			// Post-hoc consume if the pre-execution gate was skipped (bare check->record still
			// enforces action binding + single-execution; the STRONG pre-execution freshness
			// guarantee requires consume()). Then atomically COMPLETE, at most once (INV-08).
			if a.State == authz.Issued {
				if _, cerr := s.store.Consume(authID, now.Unix()); cerr != nil && !errors.Is(cerr, authz.ErrAlreadyConsumed) {
					return errReply("record: authorization store unavailable (fail closed): " + cerr.Error())
				}
				if rec.Supervision != nil {
					rec.Supervision.ConsumedAtUnix = now.Unix()
				}
			}
			if _, compErr := s.store.Complete(authID, now.Unix()); compErr != nil {
				if errors.Is(compErr, authz.ErrConflict) {
					return errReply(fmt.Sprintf(
						"authorization %s is already recorded as executed; refusing a duplicate execution confirmation (idempotency)", authID))
				}
				return errReply("record: authorization store unavailable (fail closed): " + compErr.Error())
			}
			if rec.Supervision != nil {
				rec.Supervision.AuthState = string(authz.Completed)
				rec.Supervision.ExecutedAtUnix = now.Unix() // D3: distinct from verdict time
				s.auditEvent("completed", rec)
			}
		}
	}

	rec.Executed = executed
	rep("executed")
	if rec.Supervision != nil {
		rec.Supervision.Executed = &executed
	}
	return map[string]any{"ok": true, "decision_id": rec.ID}
}

// finish feeds the reported decisions into the UNCHANGED telemetry.Builder (which re-derives
// every aggregate) and returns the canonical RunResult plus the provenance map.
func (s *extSession) finish(c sessionCmd) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := telemetry.NewRun(s.runID, s.task)
	if s.taskType != "" {
		b.SetTaskType(s.taskType)
	}
	for _, d := range s.decisions {
		b.AddDecision(*d) // re-assigns decision_NNN in creation order (== the IDs we returned)
	}
	run := b.Finish(c.Success, c.TerminationReason, c.Output)
	// External mode: ARK did not call any provider. Report status honestly as "reported"
	// (the external runtime used it) — never "configured", which would imply ARK holds a key.
	run.Providers = map[string]string{}
	for prov := range s.providers {
		run.Providers[prov] = "reported"
	}
	return map[string]any{"ok": true, "run_result": run, "provenance": s.provenanceObj()}
}

func (s *extSession) provenanceObj() map[string]any {
	decs := map[string]any{}
	for id, p := range s.prov {
		decs[id] = &decisionProv{Reported: dedup(p.Reported), Derived: dedup(p.Derived)}
	}
	return map[string]any{
		"origin":    "external_session",
		"note":      "model/token/tool/latency/verification/routing are REPORTED by the external runtime; ARK derived ids, ordering, cost (from tokens+model), supervision verdicts, and all aggregates",
		"decisions": decs,
	}
}

func mapDecisionType(action string) telemetry.DecisionType {
	switch action {
	case "tool_call":
		return telemetry.DecisionToolCall
	case "complete":
		return telemetry.DecisionComplete
	case "retry":
		return telemetry.DecisionRetry
	case "grounding":
		return telemetry.DecisionGrounding
	case "supervision":
		return telemetry.DecisionSupervision
	case "":
		return ""
	default:
		return telemetry.DecisionType(action)
	}
}

func runSession() {
	r := bufio.NewReaderSize(os.Stdin, 1<<20)
	w := bufio.NewWriter(os.Stdout)
	defer w.Flush()
	var s *extSession
	for {
		line, err := r.ReadBytes('\n')
		if t := bytes.TrimSpace(line); len(t) > 0 {
			var c sessionCmd
			if e := json.Unmarshal(t, &c); e != nil {
				writeLine(w, errReply("bad command json: "+e.Error()))
			} else {
				switch c.Cmd {
				case "hello":
					writeLine(w, helloReply())
				case "start":
					s = newExtSession(c)
					reply := helloReply() // start advertises the compatibility contract...
					reply["run_id"] = s.runID
					writeLine(w, reply)
				case "check":
					writeLine(w, requireSession(s, func() map[string]any { return s.check(c) }))
				case "consume":
					writeLine(w, requireSession(s, func() map[string]any { return s.consume(c) }))
				case "status":
					writeLine(w, requireSession(s, func() map[string]any { return s.status(c) }))
				case "record":
					writeLine(w, requireSession(s, func() map[string]any { return s.record(c) }))
				case "finish":
					writeLine(w, requireSession(s, func() map[string]any { return s.finish(c) }))
					w.Flush()
					return
				default:
					writeLine(w, errReply("unknown cmd: "+c.Cmd))
				}
			}
			w.Flush()
		}
		if err != nil {
			return // EOF or read error ends the session
		}
	}
}

func requireSession(s *extSession, fn func() map[string]any) map[string]any {
	if s == nil {
		return errReply("no active session; send {\"cmd\":\"start\"} first")
	}
	return fn()
}

func errReply(msg string) map[string]any { return map[string]any{"error": msg} }

// dedup returns the list with duplicates removed, preserving first-seen order (a decision
// touched by both check and record may report the same field name twice).
func dedup(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, x := range in {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// writeLine emits one COMPACT JSON object per line (the session protocol is line-delimited).
func writeLine(w *bufio.Writer, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		b, _ = json.Marshal(errReply("marshal: " + err.Error()))
	}
	w.Write(b)
	w.WriteByte('\n')
}
