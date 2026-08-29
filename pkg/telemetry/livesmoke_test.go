package telemetry_test

// Live smoke: a REAL runtime.Agent.Run (real cost machinery, real result + CostReport) ->
// telemetry.FromTaskResult -> RunResult JSON, reconciled against the run's own CostReport
// (the same source the human-readable Cost Report is printed from). No API, deterministic,
// self-contained (own stubs implementing the exported runtime interfaces). Touches no WIP.

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	ctx "github.com/atripati/ark/pkg/context"
	"github.com/atripati/ark/pkg/router"
	"github.com/atripati/ark/pkg/runtime"
	"github.com/atripati/ark/pkg/telemetry"
)

type stubResp struct {
	text, tool string
	params     map[string]interface{}
	in, out    int
}

type stubExec struct {
	seq []stubResp
	i   int
}

func (e *stubExec) Execute(_ string, _ string) (*runtime.ModelResponse, error) {
	r := e.seq[len(e.seq)-1]
	if e.i < len(e.seq) {
		r = e.seq[e.i]
		e.i++
	}
	mr := &runtime.ModelResponse{Text: r.text, InputTokens: r.in, OutputTokens: r.out,
		TokensUsed: r.in + r.out, Latency: 500 * time.Millisecond}
	if r.tool != "" {
		mr.ToolCall = &runtime.ToolCall{Name: r.tool, Params: r.params}
	}
	return mr, nil
}

type stubTools struct{ results map[string]string }

func (t *stubTools) Handle(c *runtime.ToolCall) error { c.Result = t.results[c.Name]; return nil }

func approxEq(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestLiveRunToRunResultJSON(t *testing.T) {
	mgr := ctx.NewManager(ctx.DefaultBudget(200000))
	mgr.RegisterTool("github_search_repos", "github_search_repos",
		"search repos: search GitHub repositories", `{"name":"github_search_repos"}`)
	engine := ctx.NewEngine(mgr, ctx.DefaultEngineConfig())

	exec := &stubExec{seq: []stubResp{
		{tool: "github_search_repos", params: map[string]interface{}{"query": "python web frameworks", "sort": "stars"}, in: 449, out: 16},
		{text: "Based on the GitHub search results:\n1. Django - 89028 stars - https://github.com/django/django\n" +
			"2. Flask - 72145 stars - https://github.com/pallets/flask\n3. Tornado - 22179 stars - https://github.com/tornadoweb/tornado",
			in: 882, out: 186},
	}}
	tools := &stubTools{results: map[string]string{
		"github_search_repos": `[{"name":"django","stars":89028},{"name":"flask","stars":72145},{"name":"tornado","stars":22179}]`}}

	agent := runtime.NewAgent(engine, exec, tools,
		runtime.AgentConfig{Provider: "openai", Model: "gpt-4o", MaxSteps: 10, MaxRetriesPerTool: 3,
			TotalTimeout: 30 * time.Second, Verbose: false})

	task := "find the top 3 most popular Python web frameworks on GitHub"
	result := agent.Run("ark-run", task)

	if result.CostReport == nil {
		t.Fatal("real run must produce a CostReport")
	}

	// Real router decisions would come from modelRouter.Decisions() in runAgent; here we
	// supply the actual router.Decision type per priced step to prove the routing join.
	var routing []router.Decision
	for _, sc := range result.CostReport.Steps {
		routing = append(routing, router.Decision{Step: sc.Step, ModelUsed: "gpt-4o",
			Reason: "final reasoning/summary benefits from strong model"})
	}

	rr := telemetry.FromTaskResult(task, result, routing)

	// --- reconcile with the run's own CostReport (the human-readable cost-report source) ---
	if !approxEq(rr.TotalCost, result.CostReport.TotalCost) {
		t.Fatalf("RunResult total %.8f != CostReport total %.8f", rr.TotalCost, result.CostReport.TotalCost)
	}
	if len(rr.Decisions) != len(result.CostReport.Steps) {
		t.Fatalf("decision count %d != cost report steps %d", len(rr.Decisions), len(result.CostReport.Steps))
	}
	var sum float64
	for _, d := range rr.Decisions {
		sum += d.Cost.TotalCost
	}
	if !approxEq(sum, rr.TotalCost) {
		t.Fatalf("run total != sum of decisions (%.8f vs %.8f)", rr.TotalCost, sum)
	}
	for tool, c := range result.CostReport.CostByTool {
		if !approxEq(rr.CostByTool[tool], c) {
			t.Fatalf("cost_by_tool[%s] %.8f != CostReport %.8f", tool, rr.CostByTool[tool], c)
		}
	}
	for action, c := range result.CostReport.CostByAction {
		if !approxEq(rr.CostByAction[action], c) {
			t.Fatalf("cost_by_action[%s] %.8f != CostReport %.8f", action, rr.CostByAction[action], c)
		}
	}
	// stable ids + routing reason came from the actual router.Decision
	if rr.Decisions[0].ID != "decision_001" {
		t.Fatalf("unstable id: %s", rr.Decisions[0].ID)
	}
	if rr.Decisions[0].RoutingReason == "" || rr.Decisions[0].Model != "gpt-4o" {
		t.Fatalf("routing not attached from router.Decision: %+v", rr.Decisions[0])
	}

	// JSON round-trips and leaks no secret material
	j, err := rr.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(j), "sk-") || strings.Contains(strings.ToLower(string(j)), "authorization") {
		t.Fatal("telemetry JSON leaked secret-looking material")
	}
	var back telemetry.RunResult
	if err := json.Unmarshal(j, &back); err != nil || back.RunID != rr.RunID || len(back.Decisions) != len(rr.Decisions) {
		t.Fatalf("JSON round-trip failed: err=%v", err)
	}

	t.Logf("live RunResult reconciles: total=$%.6f tokens=%d decisions=%d (CostReport total=$%.6f)",
		rr.TotalCost, rr.TotalTokens, len(rr.Decisions), result.CostReport.TotalCost)
	t.Logf("RunResult JSON:\n%s", string(j))
}
