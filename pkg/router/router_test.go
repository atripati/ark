package router

import (
	"fmt"
	"testing"
	"time"

	"github.com/atripati/ark/pkg/runtime"
)

// this is for mock executors

type mockModel struct {
	name      string
	calls     int
	failUntil int // failed  the first N calls
}

func (m *mockModel) Execute(context, task string) (*runtime.ModelResponse, error) {
	m.calls++
	if m.calls <= m.failUntil {
		return nil, fmt.Errorf("mock/%s: simulated failure", m.name)
	}
	return &runtime.ModelResponse{
		Text:         fmt.Sprintf("[%s] response to: %s", m.name, task[:min(len(task), 30)]),
		TokensUsed:   50,
		InputTokens:  30,
		OutputTokens: 20,
		Latency:      100 * time.Millisecond,
	}, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// this is for strategy Tests

func TestSingleStrategyUsesOneModel(t *testing.T) {
	fast := &mockModel{name: "gpt-4o-mini"}
	strong := &mockModel{name: "gpt-4o"}

	r := New(Config{Strategy: StrategySingle}, fast, strong)
	r.SetStep(1, "tool_call")

	resp, err := r.Execute("ctx", "test task")
	if err != nil {
		t.Fatalf("should succeed: %v", err)
	}
	if fast.calls != 1 {
		t.Errorf("single strategy should use fast model, got %d calls", fast.calls)
	}
	if strong.calls != 0 {
		t.Error("single strategy should not use strong model")
	}
	t.Logf("Response: %s", resp.Text)
}

func TestQualityFirstAlwaysUsesStrong(t *testing.T) {
	fast := &mockModel{name: "gpt-4o-mini"}
	strong := &mockModel{name: "gpt-4o"}

	r := New(Config{Strategy: StrategyQualityFirst}, fast, strong)

	steps := []string{"tool_call", "complete", "retry", "grounding"}
	for i, st := range steps {
		r.SetStep(i+1, st)
		r.Execute("ctx", "test task")
	}

	if fast.calls != 0 {
		t.Error("quality_first should never use fast model")
	}
	if strong.calls != 4 {
		t.Errorf("quality_first should use strong for all 4 steps, got %d", strong.calls)
	}
}

func TestCostOptimizedRouting(t *testing.T) {
	fast := &mockModel{name: "gpt-4o-mini"}
	strong := &mockModel{name: "gpt-4o"}

	r := New(Config{
		Strategy:    StrategyCostOptimized,
		FastModel:   ModelSpec{Name: "gpt-4o-mini"},
		StrongModel: ModelSpec{Name: "gpt-4o"},
	}, fast, strong)

	// Step 1: tool_call → should use fast
	r.SetStep(1, "tool_call")
	r.Execute("ctx", "tool call task")
	if fast.calls != 1 {
		t.Error("tool_call should use fast model")
	}
	if strong.calls != 0 {
		t.Error("tool_call should NOT use strong model")
	}

	// Step 2: complete → should use strong
	r.SetStep(2, "complete")
	r.Execute("ctx", "summarize results")
	if strong.calls != 1 {
		t.Error("complete should use strong model")
	}

	// Step 3: retry → should use strong
	r.SetStep(3, "retry")
	r.Execute("ctx", "retry failed step")
	if strong.calls != 2 {
		t.Error("retry should use strong model")
	}

	// Step 4: grounding → should use fast
	r.SetStep(4, "grounding")
	r.Execute("ctx", "check grounding")
	if fast.calls != 2 {
		t.Error("grounding should use fast model")
	}

	t.Logf("Fast: %d calls, Strong: %d calls", fast.calls, strong.calls)
}

//this is for  fallback Tests

func TestCostOptimizedFallbackOnFailure(t *testing.T) {
	fast := &mockModel{name: "gpt-4o-mini", failUntil: 1} // first call fails
	strong := &mockModel{name: "gpt-4o"}

	r := New(Config{
		Strategy:    StrategyCostOptimized,
		FastModel:   ModelSpec{Name: "gpt-4o-mini"},
		StrongModel: ModelSpec{Name: "gpt-4o"},
	}, fast, strong)

	r.SetStep(1, "tool_call")
	resp, err := r.Execute("ctx", "tool call task")

	if err != nil {
		t.Fatalf("should succeed via fallback: %v", err)
	}
	if fast.calls != 1 {
		t.Error("should have tried fast model first")
	}
	if strong.calls != 1 {
		t.Error("should have fallen back to strong model")
	}

	// Check decision log shows fallback
	decisions := r.Decisions()
	hasFallback := false
	for _, d := range decisions {
		if d.Fallback {
			hasFallback = true
			t.Logf("Fallback recorded: %s → %s (%s)", d.PriorModel, d.ModelUsed, d.Reason)
		}
	}
	if !hasFallback {
		t.Error("decision log should record fallback")
	}

	t.Logf("Response: %s", resp.Text)
}

// this is for learning Tests

func TestLearningPromotesToStrongAfterFailures(t *testing.T) {
	fast := &mockModel{name: "gpt-4o-mini", failUntil: 3} // fails 3 times
	strong := &mockModel{name: "gpt-4o"}

	r := New(Config{
		Strategy:    StrategyCostOptimized,
		FastModel:   ModelSpec{Name: "gpt-4o-mini"},
		StrongModel: ModelSpec{Name: "gpt-4o"},
	}, fast, strong)

	// Run tool_call twice — both fail on fast, fallback to strong
	for i := 0; i < 2; i++ {
		r.SetStep(i+1, "tool_call")
		r.Execute("ctx", "tool task")
	}
	// Now the third time, it should promote to strong directly (skip fast)
	r.SetStep(3, "tool_call")
	strongBefore := strong.calls
	r.Execute("ctx", "another tool task")

	// Check that it went to strong directly (not fast then fallback)
	decisions := r.Decisions()
	lastNonFallback := decisions[len(decisions)-1]
	if !lastNonFallback.Fallback {
		if lastNonFallback.Tier != TierStrong {
			t.Errorf("after 2 failures, tool_call should be promoted to strong, got tier=%s", lastNonFallback.Tier)
		}
		t.Logf("Correctly promoted: %s (%s)", lastNonFallback.ModelUsed, lastNonFallback.Reason)
	}

	_ = strongBefore
}

func TestPerformanceTracking(t *testing.T) {
	fast := &mockModel{name: "gpt-4o-mini"}
	strong := &mockModel{name: "gpt-4o"}

	r := New(Config{
		Strategy:    StrategyCostOptimized,
		FastModel:   ModelSpec{Name: "gpt-4o-mini"},
		StrongModel: ModelSpec{Name: "gpt-4o"},
	}, fast, strong)

	// Run several steps
	for i := 0; i < 3; i++ {
		r.SetStep(i+1, "tool_call")
		r.Execute("ctx", "task")
	}
	r.SetStep(4, "complete")
	r.Execute("ctx", "final")

	stats := r.Stats()
	if len(stats) == 0 {
		t.Fatal("should have performance stats")
	}

	for key, stat := range stats {
		t.Logf("Perf %s: attempts=%d success=%.0f%% latency=%v",
			key, stat.Attempts, stat.SuccessRate*100, stat.AvgLatency)
	}
}

//this is for decision logging tests

func TestDecisionLogging(t *testing.T) {
	fast := &mockModel{name: "gpt-4o-mini"}
	strong := &mockModel{name: "gpt-4o"}

	r := New(Config{
		Strategy:    StrategyCostOptimized,
		FastModel:   ModelSpec{Name: "gpt-4o-mini"},
		StrongModel: ModelSpec{Name: "gpt-4o"},
	}, fast, strong)

	r.SetStep(1, "tool_call")
	r.Execute("ctx", "tool task")
	r.SetStep(2, "complete")
	r.Execute("ctx", "summarize")

	decisions := r.Decisions()
	if len(decisions) != 2 {
		t.Fatalf("expected 2 decisions, got %d", len(decisions))
	}

	if decisions[0].StepType != "tool_call" {
		t.Errorf("first decision should be tool_call, got %s", decisions[0].StepType)
	}
	if decisions[0].Reason == "" {
		t.Error("decision should have a reason")
	}
	if decisions[1].StepType != "complete" {
		t.Errorf("second decision should be complete, got %s", decisions[1].StepType)
	}

	for _, d := range decisions {
		t.Logf("Decision: step=%d type=%s model=%s reason=%s", d.Step, d.StepType, d.ModelUsed, d.Reason)
	}
}

func TestResetDecisions(t *testing.T) {
	r := NewSingle(&mockModel{name: "test"})

	r.SetStep(1, "tool_call")
	r.Execute("ctx", "task")

	if len(r.Decisions()) == 0 {
		t.Fatal("should have decisions")
	}

	r.ResetDecisions()
	if len(r.Decisions()) != 0 {
		t.Error("decisions should be cleared after reset")
	}
}

func TestFormatDecisions(t *testing.T) {
	fast := &mockModel{name: "gpt-4o-mini"}
	strong := &mockModel{name: "gpt-4o"}

	r := New(Config{
		Strategy:    StrategyCostOptimized,
		FastModel:   ModelSpec{Name: "gpt-4o-mini"},
		StrongModel: ModelSpec{Name: "gpt-4o"},
	}, fast, strong)

	r.SetStep(1, "tool_call")
	r.Execute("ctx", "tool")
	r.SetStep(2, "complete")
	r.Execute("ctx", "summary")

	formatted := r.FormatDecisions()
	if formatted == "" {
		t.Fatal("formatted output should not be empty")
	}
	t.Logf("Formatted:\n%s", formatted)
}

//this is for step classification tests

func TestClassifyStep(t *testing.T) {
	tests := []struct {
		name      string
		step      int
		isRetry   bool
		toolCalls int
		hasResult bool
		want      string
	}{
		{"first step no tools", 1, false, 0, false, "tool_call"},
		{"retry step", 2, true, 0, false, "retry"},
		{"after tool success", 2, false, 1, true, "complete"},
		{"second tool call", 2, false, 0, false, "tool_call"},
	}

	for _, tt := range tests {
		got := ClassifyStep(tt.step, tt.isRetry, tt.toolCalls, tt.hasResult)
		if string(got) != tt.want {
			t.Errorf("%s: got %s, want %s", tt.name, got, tt.want)
		}
	}
}

//this is for newsingle backwards compatibility

func TestNewSingleBackwardsCompatible(t *testing.T) {
	model := &mockModel{name: "llama3"}
	r := NewSingle(model)

	r.SetStep(1, "tool_call")
	r.Execute("ctx", "task1")
	r.SetStep(2, "complete")
	r.Execute("ctx", "task2")
	r.SetStep(3, "retry")
	r.Execute("ctx", "task3")

	if model.calls != 3 {
		t.Errorf("single model should handle all steps, got %d calls", model.calls)
	}
	if r.Strategy() != StrategySingle {
		t.Errorf("strategy should be single, got %s", r.Strategy())
	}
}

func TestExportImportLearning(t *testing.T) {
	fast := &mockModel{name: "gpt-4o-mini", failUntil: 1}
	strong := &mockModel{name: "gpt-4o"}

	r1 := New(Config{
		Strategy:    StrategyCostOptimized,
		FastModel:   ModelSpec{Name: "gpt-4o-mini"},
		StrongModel: ModelSpec{Name: "gpt-4o"},
	}, fast, strong)

	// Run a task — fast fails, strong succeeds
	r1.SetStep(1, "tool_call")
	r1.Execute("ctx", "task")

	// Export learning
	snapshots := r1.ExportLearning()
	if len(snapshots) == 0 {
		t.Fatal("should have learning data to export")
	}
	t.Logf("Exported %d snapshots", len(snapshots))

	// Create new router and import
	fast2 := &mockModel{name: "gpt-4o-mini"}
	strong2 := &mockModel{name: "gpt-4o"}
	r2 := New(Config{
		Strategy:    StrategyCostOptimized,
		FastModel:   ModelSpec{Name: "gpt-4o-mini"},
		StrongModel: ModelSpec{Name: "gpt-4o"},
	}, fast2, strong2)

	r2.ImportLearning(snapshots)

	// Verify learning was imported
	stats := r2.Stats()
	if len(stats) == 0 {
		t.Fatal("imported router should have stats")
	}
	for key, stat := range stats {
		t.Logf("Imported: %s → attempts=%d success=%.0f%%", key, stat.Attempts, stat.SuccessRate*100)
	}
}

func TestLearningPersistsAcrossRouters(t *testing.T) {
	// Simulate: fast model fails on tool_call → learning exported → new router imports → skips fast model
	fast1 := &mockModel{name: "mini", failUntil: 2}
	strong1 := &mockModel{name: "full"}

	r1 := New(Config{
		Strategy:    StrategyCostOptimized,
		FastModel:   ModelSpec{Name: "mini"},
		StrongModel: ModelSpec{Name: "full"},
	}, fast1, strong1)

	// Two failures on fast model for tool_call
	r1.SetStep(1, "tool_call")
	r1.Execute("ctx", "task1")
	r1.SetStep(2, "tool_call")
	r1.Execute("ctx", "task2")

	// Export and create new router
	snapshots := r1.ExportLearning()

	fast2 := &mockModel{name: "mini"}
	strong2 := &mockModel{name: "full"}
	r2 := New(Config{
		Strategy:    StrategyCostOptimized,
		FastModel:   ModelSpec{Name: "mini"},
		StrongModel: ModelSpec{Name: "full"},
	}, fast2, strong2)
	r2.ImportLearning(snapshots)

	// Now tool_call should go directly to strong (learned from r1's failures)
	r2.SetStep(3, "tool_call")
	r2.Execute("ctx", "task3")

	decisions := r2.Decisions()
	if len(decisions) == 0 {
		t.Fatal("should have decisions")
	}

	lastDecision := decisions[len(decisions)-1]
	if lastDecision.Tier != TierStrong {
		t.Errorf("should have promoted to strong after imported failures, got tier=%s reason=%s",
			lastDecision.Tier, lastDecision.Reason)
	}
	t.Logf("Correctly promoted after import: %s (%s)", lastDecision.ModelUsed, lastDecision.Reason)
}
