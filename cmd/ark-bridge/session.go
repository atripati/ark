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
	"fmt"
	"os"
	"time"

	"github.com/atripati/ark/pkg/cost"
	"github.com/atripati/ark/pkg/supervise"
	"github.com/atripati/ark/pkg/telemetry"
)

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
	Action     string                   `json:"action"`
	Tool       string                   `json:"tool"`
	Constraint string                   `json:"constraint"`
	Proposed   supervise.ProposedAction `json:"proposed"`
	Evidence   supervise.Evidence       `json:"evidence"`

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
	Executed      *bool           `json:"executed"`
	Of            string          `json:"of"` // decision_NNN this telemetry completes

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
	runID       string
	task        string
	taskType    string
	provider    string
	supervision string
	budget      int
	sup         *supervise.Supervisor

	decisions []*telemetry.DecisionRecord // creation order (== Builder add order at finish)
	byID      map[string]*telemetry.DecisionRecord
	retry     map[string]int // authoritative retry counter, per constraint
	prov      map[string]*decisionProv
	providers map[string]bool // providers the external runtime reported using
	seq       int
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
	return &extSession{
		runID: runID, task: c.Task, taskType: c.TaskType, provider: prov,
		supervision: sv, budget: budget, sup: supervise.New(),
		byID: map[string]*telemetry.DecisionRecord{}, retry: map[string]int{},
		prov: map[string]*decisionProv{}, providers: map[string]bool{},
	}
}

// check evaluates a reported proposal against a runtime constraint. ARK owns the retry
// counter + verdict; it never authors the replacement action.
func (s *extSession) check(c sessionCmd) map[string]any {
	if s.supervision != "experimental" {
		return errReply("constrained supervision is experimental and off by default; open the trace with supervision='experimental'")
	}
	rc := s.retry[c.Constraint] // authoritative retry state (Go), not Python
	d := s.sup.Evaluate(supervise.Request{
		Constraint: c.Constraint, Proposed: c.Proposed, Evidence: c.Evidence,
		RetryCount: rc, Budget: s.budget,
	})
	if d.Verdict != supervise.Allow {
		s.retry[c.Constraint]++ // a non-ALLOW consumes one unit of the recovery budget
	}

	s.seq++
	id := fmt.Sprintf("decision_%03d", s.seq)
	notExec := false
	sup := telemetry.SupervisionFromDecision(d, "inline_evidence")
	sup.Executed = &notExec // not executed yet; record(of=id) flips this on ALLOW+execute
	rec := &telemetry.DecisionRecord{
		ID: id, Sequence: s.seq, DecisionType: mapDecisionType(c.Action),
		Action: c.Action, Tool: c.Tool, Supervision: sup, Executed: false,
	}
	s.decisions = append(s.decisions, rec)
	s.byID[id] = rec
	s.prov[id] = &decisionProv{
		Reported: []string{"action", "proposed", "constraint", "evidence"},
		Derived: []string{"id", "sequence", "timestamp", "supervision.verdict",
			"supervision.retry_number", "supervision.suggested_from_evidence"},
	}
	return map[string]any{
		"ok": true, "decision_id": id,
		"verdict": string(d.Verdict), "reason": d.Reason,
		"retry_number": d.Audit.RetryNumber, "suggested": d.Audit.SuggestedFromEvidence,
		"allowed": d.Verdict == supervise.Allow,
	}
}

// record attaches reported execution telemetry, either to a prior check (of=decision_NNN)
// or as a fresh decision. ARK derives only IDs/ordering/cost — never the model/tool facts.
func (s *extSession) record(c sessionCmd) map[string]any {
	var rec *telemetry.DecisionRecord
	if c.Of != "" {
		rec = s.byID[c.Of]
		if rec == nil {
			return errReply("record: unknown of=" + c.Of)
		}
	} else {
		s.seq++
		id := fmt.Sprintf("decision_%03d", s.seq)
		rec = &telemetry.DecisionRecord{ID: id, Sequence: s.seq}
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
				case "start":
					s = newExtSession(c)
					writeLine(w, map[string]any{"ok": true, "run_id": s.runID})
				case "check":
					writeLine(w, requireSession(s, func() map[string]any { return s.check(c) }))
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
