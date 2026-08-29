package telemetry

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/atripati/ark/pkg/supervise"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// Reconstruct the user's real Run 2 (top-3 Python web frameworks) from its Cost Report.
func run2() RunResult {
	b := NewRun("ark-run", "find the top 3 most popular Python web frameworks on GitHub").SetTaskType("ranking")
	b.AddDecision(DecisionRecord{
		DecisionType: DecisionToolCall, Action: "tool_call", Tool: "github_search_repos", Model: "gpt-4o",
		RoutingReason: "promoted: fast model has 57% success rate for tool_call (threshold: 60%)",
		Cost:          Cost{InputTokens: 449, OutputTokens: 16, ModelCost: 0.001283, TotalCost: 0.001283},
		LatencyMs:     2526, Executed: true, Outcome: "success",
	})
	b.AddDecision(DecisionRecord{
		DecisionType: DecisionComplete, Action: "complete", Model: "gpt-4o",
		RoutingReason: "final reasoning/summary benefits from strong model",
		Cost:          Cost{InputTokens: 882, OutputTokens: 186, ModelCost: 0.004065, TotalCost: 0.004065},
		LatencyMs:     2005, Executed: true, Outcome: "complete",
		Verification: &Verification{Method: "structural", Score: f(0.80), Passed: bptr(true)},
	})
	return b.Finish(true, "complete", "1. Django ... 2. Flask ... 3. Tornado ...")
}

func f(v float64) *float64 { return &v }
func bptr(v bool) *bool    { return &v }

func TestStableDecisionIDsAndOrder(t *testing.T) {
	b := NewRun("r", "t")
	id1 := b.AddDecision(DecisionRecord{DecisionType: DecisionToolCall})
	id2 := b.AddDecision(DecisionRecord{DecisionType: DecisionComplete})
	if id1 != "decision_001" || id2 != "decision_002" {
		t.Fatalf("stable IDs wrong: %s %s", id1, id2)
	}
	r := b.Finish(true, "complete", "")
	if r.Decisions[0].Sequence != 1 || r.Decisions[1].Sequence != 2 {
		t.Fatal("sequence not monotonic")
	}
}

func TestRunTotalEqualsSumOfDecisions_NoDoubleCount(t *testing.T) {
	r := run2()
	var sum float64
	var toks int
	for _, d := range r.Decisions {
		sum += d.Cost.TotalCost
		toks += d.Cost.InputTokens + d.Cost.OutputTokens
	}
	if !approx(r.TotalCost, sum) {
		t.Fatalf("run total %.6f != sum of decisions %.6f", r.TotalCost, sum)
	}
	if r.TotalTokens != toks {
		t.Fatalf("tokens double/under-counted: run=%d sum=%d", r.TotalTokens, toks)
	}
}

func TestReconcileWithOldCostReport(t *testing.T) {
	r := run2()
	if !approx(r.TotalCost, 0.005348) {
		t.Fatalf("total cost %.6f != old Cost Report 0.005348", r.TotalCost)
	}
	if r.TotalTokens != 1533 {
		t.Fatalf("total tokens %d != 1533", r.TotalTokens)
	}
	if !approx(r.CostByTool["github_search_repos"], 0.001283) {
		t.Fatalf("cost_by_tool github %.6f != 0.001283", r.CostByTool["github_search_repos"])
	}
	if !approx(r.CostByAction["tool_call"], 0.001283) || !approx(r.CostByAction["complete"], 0.004065) {
		t.Fatalf("cost_by_action mismatch: %+v", r.CostByAction)
	}
	// by_model partitions the total (single model here)
	if !approx(r.CostByModel["gpt-4o"], 0.005348) {
		t.Fatalf("cost_by_model gpt-4o %.6f != total", r.CostByModel["gpt-4o"])
	}
	// by_action partitions the total; by_tool is the tool-associated subset
	var byAction float64
	for _, v := range r.CostByAction {
		byAction += v
	}
	if !approx(byAction, r.TotalCost) {
		t.Fatalf("cost_by_action must sum to total: %.6f vs %.6f", byAction, r.TotalCost)
	}
}

func TestRetryDecisionsAreDistinct(t *testing.T) {
	b := NewRun("r", "t")
	b.AddDecision(DecisionRecord{DecisionType: DecisionToolCall, Action: "tool_call", Model: "gpt-4o-mini",
		Cost: Cost{ModelCost: 0.0001, TotalCost: 0.0001}})
	b.AddDecision(DecisionRecord{DecisionType: DecisionRetry, Action: "retry", Model: "gpt-4o",
		Cost: Cost{ModelCost: 0.002, TotalCost: 0.002}})
	r := b.Finish(true, "complete", "")
	if len(r.Decisions) != 2 || r.Decisions[0].ID == r.Decisions[1].ID {
		t.Fatal("retry must be a distinct record")
	}
	if !approx(r.CostByAction["retry"], 0.002) || !approx(r.CostByAction["tool_call"], 0.0001) {
		t.Fatalf("retry cost not attributed distinctly: %+v", r.CostByAction)
	}
}

func TestSupervisedRejectRetryAllowChain(t *testing.T) {
	b := NewRun("r", "book")
	// proposal 1: rank-1 -> REJECT (retry 0), not executed
	rej := 0
	no := false
	b.AddDecision(DecisionRecord{DecisionType: DecisionToolCall, Action: "tool_call", Tool: "book", Model: "gpt-4o",
		Cost: Cost{ModelCost: 0.001, TotalCost: 0.001}, Executed: false,
		Supervision: &Supervision{ApplicableConstraint: "rank", Verdict: "REJECT", RejectionReason: "rank 1 != 2",
			RetryNumber: &rej, SuggestedFromEvidence: "B", Executed: &no}})
	// proposal 2: rank-2 -> ALLOW, executed
	yes := true
	one := 1
	b.AddDecision(DecisionRecord{DecisionType: DecisionToolCall, Action: "tool_call", Tool: "book", Model: "gpt-4o",
		Cost: Cost{ModelCost: 0.001, TotalCost: 0.001}, Executed: true,
		Supervision: &Supervision{ApplicableConstraint: "rank", Verdict: "ALLOW", RetryNumber: &one, Executed: &yes}})
	r := b.Finish(true, "complete", "")

	if !r.Supervision.Enabled || r.Supervision.Interventions != 1 {
		t.Fatalf("expected 1 intervention (REJECT); got enabled=%v n=%d", r.Supervision.Enabled, r.Supervision.Interventions)
	}
	if r.Supervision.ByVerdict["REJECT"] != 1 || r.Supervision.ByVerdict["ALLOW"] != 1 {
		t.Fatalf("verdict counts wrong: %+v", r.Supervision.ByVerdict)
	}
	if !approx(r.CostBySupervision["REJECT"], 0.001) || !approx(r.CostBySupervision["ALLOW"], 0.001) {
		t.Fatalf("cost_by_supervision wrong: %+v", r.CostBySupervision)
	}
	if !*r.Decisions[1].Supervision.Executed { // final ALLOW executed
		t.Fatal("final ALLOW should be executed")
	}
}

func TestSupervisionBridgeFromPkgSupervise(t *testing.T) {
	d := supervise.New().Evaluate(supervise.Request{Constraint: "rank",
		Proposed: supervise.ProposedAction{Option: "A"},
		Evidence: supervise.Evidence{RequestedRank: 2, EvidenceComplete: true,
			Options: []supervise.Option{{ID: "A", Price: 163, IsDirect: true}, {ID: "B", Price: 290}}},
		RetryCount: 0, Budget: 4})
	s := SupervisionFromDecision(d, "evidence#42")
	if s.Verdict != "REJECT" || s.SuggestedFromEvidence != "B" || s.TrustedEvidenceRef != "evidence#42" {
		t.Fatalf("bridge mismapped: %+v", s)
	}
	if s.Executed == nil || *s.Executed {
		t.Fatal("rejected supervise decision must map executed=false")
	}
}

func TestSupervisionDisabledRunWorks(t *testing.T) {
	r := run2() // no supervision decisions
	if r.Supervision.Enabled || r.Supervision.Interventions != 0 || r.CostBySupervision != nil {
		t.Fatalf("supervision-free run must report disabled/empty: %+v", r.Supervision)
	}
}

func TestNoHistoricalContamination(t *testing.T) {
	// RunResult is THIS RUN ONLY: the two decisions we added, no lifetime counts.
	r := run2()
	if len(r.Decisions) != 2 {
		t.Fatalf("run should contain exactly the decisions added, got %d", len(r.Decisions))
	}
	if r.Routing.ByModel["gpt-4o"] != 2 { // this run's 2 decisions, not a cumulative 15
		t.Fatalf("routing by_model must reflect THIS run (2), got %d", r.Routing.ByModel["gpt-4o"])
	}
	if r.Tools.Calls != 1 { // this run made 1 tool call, not a lifetime 7
		t.Fatalf("tool calls must be this-run (1), got %d", r.Tools.Calls)
	}
}

func TestJSONRoundTrips(t *testing.T) {
	r := run2()
	b, err := r.JSON()
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseRunResult(b)
	if err != nil {
		t.Fatal(err)
	}
	if back.RunID != r.RunID || !approx(back.TotalCost, r.TotalCost) ||
		len(back.Decisions) != len(r.Decisions) || back.Decisions[0].ID != "decision_001" {
		t.Fatalf("round-trip lost data: %+v", back)
	}
}

func TestSecretsNeverSerialized(t *testing.T) {
	red := RedactToolArgs(map[string]any{
		"query": "python web frameworks", "api_key": "sk-proj-SECRET", "Authorization": "Bearer XYZ", "limit": 3,
	})
	if red["api_key"] != "***" || red["Authorization"] != "***" || red["query"] != "python web frameworks" {
		t.Fatalf("redaction failed: %+v", red)
	}
	if ProviderStatus(true) != "configured" || ProviderStatus(false) != "absent" {
		t.Fatal("provider status must be secret-free strings")
	}
	// end-to-end: a run carrying only redacted args + provider status must not contain the secret
	b := NewRun("r", "t").SetProvider("openai", ProviderStatus(true))
	args, _ := json.Marshal(red)
	b.AddDecision(DecisionRecord{DecisionType: DecisionToolCall, Tool: "github_search_repos", ToolArgsRef: string(args)})
	out, _ := b.Finish(true, "complete", "").JSON()
	if strings.Contains(string(out), "sk-proj-SECRET") || strings.Contains(string(out), "Bearer XYZ") {
		t.Fatal("serialized telemetry leaked a secret")
	}
}
