package cost

import (
	"encoding/json"
	"testing"
	"time"
)

func TestModelPricingKnown(t *testing.T) {
	tests := []struct {
		provider, model string
		wantInput       float64
		wantZero        bool
	}{
		{"anthropic", "claude-sonnet-4-20250514", 3.0 / 1_000_000, false},
		{"openai", "gpt-4o", 2.50 / 1_000_000, false},
		{"ollama", "llama3", 0, true},
		{"ollama", "llama3.2:1b", 0, true},
		{"unknown", "unknown-model", 0, true},
	}

	for _, tt := range tests {
		p := ModelPricing(tt.provider, tt.model)
		if tt.wantZero && p.InputPerToken != 0 {
			t.Errorf("%s/%s: expected zero cost, got %f", tt.provider, tt.model, p.InputPerToken)
		}
		if !tt.wantZero && p.InputPerToken != tt.wantInput {
			t.Errorf("%s/%s: expected input price %f, got %f", tt.provider, tt.model, tt.wantInput, p.InputPerToken)
		}
	}
}

func TestCustomPricing(t *testing.T) {
	p := CustomPricing("mycloud", "my-model", 1.0, 5.0)
	if p.InputPerToken != 1.0/1_000_000 {
		t.Errorf("expected %f, got %f", 1.0/1_000_000, p.InputPerToken)
	}
	if p.OutputPerToken != 5.0/1_000_000 {
		t.Errorf("expected %f, got %f", 5.0/1_000_000, p.OutputPerToken)
	}
}

func TestTrackerRecordStep(t *testing.T) {
	pricing := ModelPricing("openai", "gpt-4o")
	tracker := NewTracker(pricing)
	tracker.SetTaskID("test-task")

	sc := tracker.RecordStep(1, 500, 200, "tool_call", "github_list_repos", 2*time.Second)

	expectedInput := 500 * 2.50 / 1_000_000
	expectedOutput := 200 * 10.0 / 1_000_000
	expectedTotal := expectedInput + expectedOutput

	if abs(sc.InputCost-expectedInput) > 0.0000001 {
		t.Errorf("input cost: expected %f, got %f", expectedInput, sc.InputCost)
	}
	if abs(sc.OutputCost-expectedOutput) > 0.0000001 {
		t.Errorf("output cost: expected %f, got %f", expectedOutput, sc.OutputCost)
	}
	if abs(sc.TotalCost-expectedTotal) > 0.0000001 {
		t.Errorf("total cost: expected %f, got %f", expectedTotal, sc.TotalCost)
	}
	if sc.CostPerMs <= 0 {
		t.Error("cost per ms should be > 0 for non-zero latency")
	}
	t.Logf("Step cost: $%.6f (in: $%.6f, out: $%.6f)", sc.TotalCost, sc.InputCost, sc.OutputCost)
}

func TestTrackerRunningCost(t *testing.T) {
	tracker := NewTracker(ModelPricing("openai", "gpt-4o"))

	tracker.RecordStep(1, 100, 50, "tool_call", "tool_a", time.Second)
	tracker.RecordStep(2, 100, 200, "complete", "", time.Second)

	running := tracker.RunningCost()
	if running <= 0 {
		t.Error("running cost should be > 0 after recording steps")
	}
	t.Logf("Running cost after 2 steps: $%.6f", running)
}

func TestBudgetEnforcement(t *testing.T) {
	pricing := CustomPricing("test", "test", 100.0, 100.0)
	tracker := NewTracker(pricing)
	tracker.SetBudget(0.001)

	tracker.RecordStep(1, 5, 3, "tool_call", "tool_a", time.Second)
	if tracker.BudgetExceeded() {
		t.Error("budget should not be exceeded after small step")
	}

	tracker.RecordStep(2, 5000, 3000, "complete", "", time.Second)
	if !tracker.BudgetExceeded() {
		t.Error("budget should be exceeded after large step")
	}

	remaining := tracker.BudgetRemaining()
	if remaining != 0 {
		t.Errorf("remaining should be 0 when exceeded, got %f", remaining)
	}
}

func TestBudgetUnlimited(t *testing.T) {
	tracker := NewTracker(ModelPricing("openai", "gpt-4o"))
	tracker.RecordStep(1, 10000, 10000, "complete", "", time.Second)

	if tracker.BudgetExceeded() {
		t.Error("budget should never be exceeded when no budget is set")
	}
	if tracker.BudgetRemaining() != -1 {
		t.Errorf("remaining should be -1 when no budget set, got %f", tracker.BudgetRemaining())
	}
}

func TestOllamaZeroCost(t *testing.T) {
	tracker := NewTracker(ModelPricing("ollama", "llama3"))
	tracker.SetTaskID("local-task")

	tracker.RecordStep(1, 500, 1200, "tool_call", "github_list_repos", 30*time.Second)
	tracker.RecordStep(2, 0, 800, "complete", "", 10*time.Second)

	report := tracker.Report()
	if report.TotalCost != 0 {
		t.Errorf("ollama should have zero cost, got $%.6f", report.TotalCost)
	}
	if report.TotalTokens != 2500 {
		t.Errorf("expected 2500 total tokens, got %d", report.TotalTokens)
	}
	t.Logf("Ollama: %d tokens, $%.6f (correct: free)", report.TotalTokens, report.TotalCost)
}

func TestReport(t *testing.T) {
	tracker := NewTracker(ModelPricing("anthropic", "claude-sonnet-4-20250514"))
	tracker.SetTaskID("report-test")
	tracker.SetAttribution("user-123", "repo-analysis", "session-abc")
	tracker.SetBudget(0.01)

	tracker.RecordStep(1, 400, 0, "tool_call", "github_list_repos", 2*time.Second)
	tracker.RecordStep(2, 0, 1100, "complete", "", 3*time.Second)

	report := tracker.Report()

	if report.TaskID != "report-test" {
		t.Errorf("expected task_id=report-test, got %s", report.TaskID)
	}
	if report.Attribution.UserID != "user-123" {
		t.Errorf("expected user_id=user-123, got %s", report.Attribution.UserID)
	}
	if report.TotalCost <= 0 {
		t.Error("total cost should be > 0 for Anthropic model")
	}
	if len(report.Steps) != 2 {
		t.Errorf("expected 2 steps, got %d", len(report.Steps))
	}
	if report.BudgetLimit != 0.01 {
		t.Errorf("expected budget 0.01, got %f", report.BudgetLimit)
	}
	if report.BudgetUsedPct <= 0 {
		t.Error("budget used pct should be > 0")
	}
	if _, ok := report.CostByTool["github_list_repos"]; !ok {
		t.Error("cost_by_tool should include github_list_repos")
	}
	if _, ok := report.CostByAction["tool_call"]; !ok {
		t.Error("cost_by_action should include tool_call")
	}

	t.Logf("Report: total=$%.6f, budget=%.1f%% used, steps=%d",
		report.TotalCost, report.BudgetUsedPct, len(report.Steps))
}

func TestReportJSON(t *testing.T) {
	tracker := NewTracker(ModelPricing("openai", "gpt-4o"))
	tracker.SetTaskID("json-test")
	tracker.RecordStep(1, 100, 200, "complete", "", time.Second)

	report := tracker.Report()
	jsonStr, err := report.JSON()
	if err != nil {
		t.Fatalf("JSON error: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if parsed["task_id"] != "json-test" {
		t.Errorf("expected task_id=json-test in JSON")
	}
	t.Logf("JSON length: %d bytes", len(jsonStr))
}

func TestReportSummary(t *testing.T) {
	tracker := NewTracker(ModelPricing("anthropic", "claude-sonnet-4-20250514"))
	tracker.SetTaskID("summary-test")
	tracker.SetAttribution("user-42", "code-review", "")
	tracker.SetBudget(0.05)

	tracker.RecordStep(1, 500, 100, "tool_call", "github_list_repos", 2*time.Second)
	tracker.RecordStep(2, 200, 800, "complete", "", 5*time.Second)

	report := tracker.Report()
	summary := report.Summary()

	if summary == "" {
		t.Fatal("summary should not be empty")
	}
	for _, want := range []string{"Cost Report", "Decision Cost Graph", "Total Cost", "Step 1", "Step 2"} {
		if !contains(summary, want) {
			t.Errorf("summary missing %q", want)
		}
	}
	t.Logf("Summary:\n%s", summary)
}

func TestAggregation(t *testing.T) {
	agg := NewAggregate()

	t1 := NewTracker(ModelPricing("openai", "gpt-4o"))
	t1.SetTaskID("task-1")
	t1.SetAttribution("user-a", "feature-x", "")
	t1.RecordStep(1, 100, 200, "tool_call", "github_list_repos", time.Second)
	agg.Add(t1.Report())

	t2 := NewTracker(ModelPricing("openai", "gpt-4o"))
	t2.SetTaskID("task-2")
	t2.SetAttribution("user-b", "feature-x", "")
	t2.RecordStep(1, 200, 400, "tool_call", "github_get_repo", time.Second)
	agg.Add(t2.Report())

	t3 := NewTracker(ModelPricing("openai", "gpt-4o"))
	t3.SetTaskID("task-3")
	t3.SetAttribution("user-a", "feature-y", "")
	t3.RecordStep(1, 50, 100, "complete", "", time.Second)
	agg.Add(t3.Report())

	if agg.TaskCount() != 3 {
		t.Errorf("expected 3 tasks, got %d", agg.TaskCount())
	}
	if agg.TotalCost() <= 0 {
		t.Error("total cost should be > 0")
	}

	byFeature := agg.CostByFeature()
	if len(byFeature) != 2 {
		t.Errorf("expected 2 features, got %d", len(byFeature))
	}
	if byFeature["feature-x"] <= 0 {
		t.Error("feature-x cost should be > 0")
	}

	byUser := agg.CostByUser()
	if len(byUser) != 2 {
		t.Errorf("expected 2 users, got %d", len(byUser))
	}

	byTool := agg.CostByTool()
	if _, ok := byTool["github_list_repos"]; !ok {
		t.Error("should have cost for github_list_repos")
	}

	avg := agg.AvgCostPerTask()
	if avg <= 0 {
		t.Error("avg cost should be > 0")
	}

	t.Logf("Aggregate: %d tasks, total=$%.6f, avg=$%.6f", agg.TaskCount(), agg.TotalCost(), avg)
	t.Logf("By feature: %v", byFeature)
	t.Logf("By user: %v", byUser)
}

func TestToolCostEfficiency(t *testing.T) {
	agg := NewAggregate()

	for i := 0; i < 2; i++ {
		tr := NewTracker(ModelPricing("openai", "gpt-4o"))
		tr.RecordStep(1, 100, 50, "tool_call", "cheap_tool", time.Second)
		agg.Add(tr.Report())
	}

	for i := 0; i < 2; i++ {
		tr := NewTracker(ModelPricing("openai", "gpt-4o"))
		tr.RecordStep(1, 500, 300, "tool_call_retry", "expensive_tool", time.Second)
		agg.Add(tr.Report())
	}
	tr := NewTracker(ModelPricing("openai", "gpt-4o"))
	tr.RecordStep(1, 500, 300, "tool_call", "expensive_tool", time.Second)
	agg.Add(tr.Report())

	efficiency := agg.ToolCostEfficiency()

	cheapEff := efficiency["cheap_tool"]
	expensiveEff := efficiency["expensive_tool"]

	if cheapEff >= expensiveEff {
		t.Errorf("cheap_tool efficiency (%f) should be better (lower) than expensive_tool (%f)",
			cheapEff, expensiveEff)
	}

	t.Logf("Efficiency: cheap=$%.6f/success, expensive=$%.6f/success", cheapEff, expensiveEff)
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func TestBudgetStopsExecution(t *testing.T) {
	pricing := CustomPricing("test", "test", 1000.0, 1000.0)
	tracker := NewTracker(pricing)
	tracker.SetBudget(0.003)

	tracker.RecordStep(1, 1000, 500, "tool_call", "tool_a", time.Second)
	if tracker.BudgetExceeded() {
		t.Log("Budget exceeded after step 1 — budget is very tight")
	}

	tracker.RecordStep(2, 2000, 1000, "complete", "", time.Second)
	if !tracker.BudgetExceeded() {
		t.Error("budget should be exceeded after step 2")
	}

	report := tracker.Report()
	if !report.BudgetExceeded {
		t.Error("report should show budget exceeded")
	}
	if report.BudgetUsedPct < 100 {
		t.Errorf("budget used should be >= 100%%, got %.1f%%", report.BudgetUsedPct)
	}

	t.Logf("Budget test: limit=$%.4f, used=$%.4f (%.0f%%), exceeded=%v",
		report.BudgetLimit, report.TotalCost, report.BudgetUsedPct, report.BudgetExceeded)
}

func TestCostAwarePricingComparison(t *testing.T) {
	models := []struct {
		provider, model string
		wantCheaper     bool
	}{
		{"openai", "gpt-4o-mini", true},
		{"openai", "gpt-4o", false},
		{"ollama", "llama3", true},
	}

	costs := make(map[string]float64)
	for _, m := range models {
		tracker := NewTracker(ModelPricing(m.provider, m.model))
		tracker.RecordStep(1, 500, 200, "tool_call", "test_tool", time.Second)
		report := tracker.Report()
		costs[m.provider+"/"+m.model] = report.TotalCost
		t.Logf("%s/%s: $%.6f", m.provider, m.model, report.TotalCost)
	}

	if costs["openai/gpt-4o"] <= costs["openai/gpt-4o-mini"] {
		t.Error("gpt-4o should cost more than gpt-4o-mini")
	}

	if costs["ollama/llama3"] != 0 {
		t.Errorf("ollama should be free, got $%.6f", costs["ollama/llama3"])
	}
}
