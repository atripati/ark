package governor

import (
	"os"
	"testing"
	"time"

	"github.com/atripati/ark/pkg/runtime"
)

func TestRegistryNewEmpty(t *testing.T) {
	r := NewRegistry()
	profiles := r.AllProfiles()
	if len(profiles) != 0 {
		t.Errorf("expected empty registry, got %d profiles", len(profiles))
	}
}

func TestRegistryRecordSuccess(t *testing.T) {
	r := NewRegistry()
	r.Record(Observation{
		Model:     "gpt-4o-mini",
		Kind:      ObsToolCallSuccess,
		StepType:  "tool_call",
		ToolName:  "github_list_repos",
		LatencyMs: 150,
		Cost:      0.00005,
	})

	p := r.GetProfile("gpt-4o-mini")
	if p == nil {
		t.Fatal("expected profile for gpt-4o-mini")
	}
	if p.TotalCalls != 1 {
		t.Errorf("expected 1 call, got %d", p.TotalCalls)
	}
	if p.TotalSuccess != 1 {
		t.Errorf("expected 1 success, got %d", p.TotalSuccess)
	}
	if p.SuccessRate != 1.0 {
		t.Errorf("expected 100%% success rate, got %.2f", p.SuccessRate)
	}
	if p.ToolCallScore <= 0.5 {
		t.Errorf("expected tool_call_score > 0.5 after success, got %.2f", p.ToolCallScore)
	}
}

func TestRegistryRecordFailure(t *testing.T) {
	r := NewRegistry()
	r.Record(Observation{
		Model:      "gpt-4o-mini",
		Kind:       ObsToolCallFailure,
		StepType:   "tool_call",
		ToolName:   "github_list_repos",
		ErrorClass: "invalid_json",
	})

	p := r.GetProfile("gpt-4o-mini")
	if p.TotalFailure != 1 {
		t.Errorf("expected 1 failure, got %d", p.TotalFailure)
	}
	if p.ToolCallScore >= 0.5 {
		t.Errorf("expected tool_call_score < 0.5 after failure, got %.2f", p.ToolCallScore)
	}
	if p.FailurePatterns["invalid_json"] != 1 {
		t.Error("expected failure pattern 'invalid_json' recorded")
	}
}

func TestRegistryHallucinationTracking(t *testing.T) {
	r := NewRegistry()
	r.Record(Observation{Model: "gpt-4o-mini", Kind: ObsToolCallSuccess, StepType: "tool_call"})
	r.Record(Observation{Model: "gpt-4o-mini", Kind: ObsHallucination, StepType: "complete", ErrorClass: "ungrounded"})

	p := r.GetProfile("gpt-4o-mini")
	if p.HallucinationCount != 1 {
		t.Errorf("expected 1 hallucination, got %d", p.HallucinationCount)
	}
	if p.HallucinationRate != 0.5 {
		t.Errorf("expected 50%% hallucination rate, got %.2f", p.HallucinationRate)
	}
	if p.ReasoningScore >= 0.5 {
		t.Errorf("expected reasoning score < 0.5 after hallucination, got %.2f", p.ReasoningScore)
	}
}

func TestRegistryToolRecords(t *testing.T) {
	r := NewRegistry()

	// 3 successes, 1 failure for github_list_repos
	r.Record(Observation{Model: "gpt-4o", Kind: ObsToolCallSuccess, ToolName: "github_list_repos", LatencyMs: 100})
	r.Record(Observation{Model: "gpt-4o", Kind: ObsToolCallSuccess, ToolName: "github_list_repos", LatencyMs: 120})
	r.Record(Observation{Model: "gpt-4o", Kind: ObsToolCallSuccess, ToolName: "github_list_repos", LatencyMs: 110})
	r.Record(Observation{Model: "gpt-4o", Kind: ObsToolCallFailure, ToolName: "github_list_repos", ErrorClass: "timeout"})

	p := r.GetProfile("gpt-4o")
	tr := p.ToolRecords["github_list_repos"]
	if tr == nil {
		t.Fatal("expected tool record for github_list_repos")
	}
	if tr.Calls != 4 {
		t.Errorf("expected 4 calls, got %d", tr.Calls)
	}
	if tr.SuccessRate != 0.75 {
		t.Errorf("expected 75%% success rate, got %.2f", tr.SuccessRate)
	}
}

func TestRegistryShouldDemote(t *testing.T) {
	r := NewRegistry()

	// 1 success, 2 failures = 33% success rate
	r.Record(Observation{Model: "gpt-4o-mini", Kind: ObsToolCallSuccess, ToolName: "web_search"})
	r.Record(Observation{Model: "gpt-4o-mini", Kind: ObsToolCallFailure, ToolName: "web_search"})
	r.Record(Observation{Model: "gpt-4o-mini", Kind: ObsToolCallFailure, ToolName: "web_search"})

	if !r.ShouldDemote("gpt-4o-mini", "web_search") {
		t.Error("expected demotion for web_search with 33% success rate")
	}

	// Tool with good success rate should not be demoted
	r.Record(Observation{Model: "gpt-4o-mini", Kind: ObsToolCallSuccess, ToolName: "github_list_repos"})
	r.Record(Observation{Model: "gpt-4o-mini", Kind: ObsToolCallSuccess, ToolName: "github_list_repos"})
	r.Record(Observation{Model: "gpt-4o-mini", Kind: ObsToolCallSuccess, ToolName: "github_list_repos"})

	if r.ShouldDemote("gpt-4o-mini", "github_list_repos") {
		t.Error("should not demote github_list_repos with 100% success rate")
	}
}

func TestRegistryBestModelFor(t *testing.T) {
	r := NewRegistry()

	// gpt-4o-mini: great at tool calls
	for i := 0; i < 10; i++ {
		r.Record(Observation{Model: "gpt-4o-mini", Kind: ObsToolCallSuccess, StepType: "tool_call", ToolName: "github_list_repos"})
	}

	// gpt-4o: great at reasoning
	for i := 0; i < 10; i++ {
		r.Record(Observation{Model: "gpt-4o", Kind: ObsReasoningSuccess, StepType: "complete"})
	}

	// gpt-4o-mini: bad at reasoning
	for i := 0; i < 5; i++ {
		r.Record(Observation{Model: "gpt-4o-mini", Kind: ObsReasoningFailure, StepType: "complete"})
	}

	candidates := []string{"gpt-4o-mini", "gpt-4o"}

	best := r.BestModelFor("tool_call", "github_list_repos", candidates)
	if best != "gpt-4o-mini" {
		t.Errorf("expected gpt-4o-mini for tool_call, got %s", best)
	}

	best = r.BestModelFor("complete", "", candidates)
	if best != "gpt-4o" {
		t.Errorf("expected gpt-4o for reasoning, got %s", best)
	}
}

func TestRegistryStepRecords(t *testing.T) {
	r := NewRegistry()

	r.Record(Observation{Model: "gpt-4o-mini", Kind: ObsToolCallSuccess, StepType: "tool_call"})
	r.Record(Observation{Model: "gpt-4o-mini", Kind: ObsToolCallSuccess, StepType: "tool_call"})
	r.Record(Observation{Model: "gpt-4o-mini", Kind: ObsReasoningFailure, StepType: "complete"})

	p := r.GetProfile("gpt-4o-mini")

	if sr, ok := p.StepRecords["tool_call"]; ok {
		if sr.SuccessRate != 1.0 {
			t.Errorf("expected 100%% success for tool_call, got %.2f", sr.SuccessRate)
		}
	} else {
		t.Error("expected step record for tool_call")
	}

	if sr, ok := p.StepRecords["complete"]; ok {
		if sr.SuccessRate != 0.0 {
			t.Errorf("expected 0%% success for complete, got %.2f", sr.SuccessRate)
		}
	} else {
		t.Error("expected step record for complete")
	}
}

func TestRegistrySaveLoad(t *testing.T) {
	r := NewRegistry()

	r.Record(Observation{Model: "gpt-4o-mini", Kind: ObsToolCallSuccess, StepType: "tool_call", ToolName: "github_list_repos", LatencyMs: 100, Cost: 0.00005})
	r.Record(Observation{Model: "gpt-4o", Kind: ObsReasoningSuccess, StepType: "complete", LatencyMs: 500, Cost: 0.001})

	path := "/tmp/test-registry.json"
	defer os.Remove(path)

	if err := r.Save(path); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	r2 := NewRegistry()
	if err := r2.Load(path); err != nil {
		t.Fatalf("load failed: %v", err)
	}

	p := r2.GetProfile("gpt-4o-mini")
	if p == nil {
		t.Fatal("expected gpt-4o-mini profile after load")
	}
	if p.TotalCalls != 1 {
		t.Errorf("expected 1 call after load, got %d", p.TotalCalls)
	}

	p2 := r2.GetProfile("gpt-4o")
	if p2 == nil {
		t.Fatal("expected gpt-4o profile after load")
	}
}

func TestRegistryLoadMissingFile(t *testing.T) {
	r := NewRegistry()
	err := r.Load("/tmp/nonexistent-registry.json")
	if err != nil {
		t.Errorf("expected no error for missing file, got %v", err)
	}
}

func TestRegistryFormatReport(t *testing.T) {
	r := NewRegistry()
	r.Record(Observation{Model: "gpt-4o-mini", Kind: ObsToolCallSuccess, StepType: "tool_call", ToolName: "github_list_repos"})

	report := r.FormatReport()
	if report == "" {
		t.Error("expected non-empty report")
	}
	if !containsStr(report, "gpt-4o-mini") {
		t.Error("expected report to contain model name")
	}
}

func TestRegistryLatencyEMA(t *testing.T) {
	r := NewRegistry()
	r.Record(Observation{Model: "gpt-4o-mini", Kind: ObsToolCallSuccess, LatencyMs: 100})
	r.Record(Observation{Model: "gpt-4o-mini", Kind: ObsToolCallSuccess, LatencyMs: 200})

	p := r.GetProfile("gpt-4o-mini")
	// EMA: 100 * 0.8 + 200 * 0.2 = 120
	if p.AvgLatencyMs < 115 || p.AvgLatencyMs > 125 {
		t.Errorf("expected EMA ~120, got %.1f", p.AvgLatencyMs)
	}
}

func TestVerifyToolCallPass(t *testing.T) {
	r := NewRegistry()
	v := NewVerifier(DefaultVerifierConfig(), r)

	result := v.VerifyToolCall("gpt-4o-mini", "github_list_repos",
		&runtime.ToolCall{
			Name:   "github_list_repos",
			Params: map[string]interface{}{"user": "atripati"},
			Result: `[{"name": "ark", "stars": 17}]`,
		},
		&runtime.ModelResponse{
			Text:    "TOOL_CALL: github_list_repos({\"user\": \"atripati\"})",
			Latency: 100 * time.Millisecond,
		},
	)

	if !result.Passed {
		t.Errorf("expected pass, got fail: %s", result.Reason)
	}
	if result.Verdict != VerdictPass {
		t.Errorf("expected VerdictPass, got %s", result.Verdict)
	}
}

func TestVerifyToolCallNilResponse(t *testing.T) {
	r := NewRegistry()
	v := NewVerifier(DefaultVerifierConfig(), r)

	result := v.VerifyToolCall("gpt-4o-mini", "github_list_repos", nil, nil)

	if result.Passed {
		t.Error("expected fail for nil response")
	}
	if result.Verdict != VerdictFail {
		t.Errorf("expected VerdictFail, got %s", result.Verdict)
	}
}

func TestVerifyToolCallEmptyOutput(t *testing.T) {
	r := NewRegistry()
	v := NewVerifier(DefaultVerifierConfig(), r)

	result := v.VerifyToolCall("gpt-4o-mini", "github_list_repos",
		&runtime.ToolCall{Name: "github_list_repos", Result: ""},
		&runtime.ModelResponse{Text: ""},
	)

	if result.Confidence >= 0.8 {
		t.Errorf("expected low confidence for empty output, got %.2f", result.Confidence)
	}
	if !containsFlag(result.Flags, "empty_output") {
		t.Error("expected 'empty_output' flag")
	}
}

func TestVerifyToolCallInvalidJSON(t *testing.T) {
	r := NewRegistry()
	v := NewVerifier(DefaultVerifierConfig(), r)

	result := v.VerifyToolCall("gpt-4o-mini", "github_list_repos",
		&runtime.ToolCall{
			Name:   "github_list_repos",
			Result: `{"broken: json}`,
		},
		&runtime.ModelResponse{Text: "Some output text here"},
	)

	if !containsFlag(result.Flags, "invalid_json_result") {
		t.Error("expected 'invalid_json_result' flag")
	}
}

func TestVerifyReasoningPass(t *testing.T) {
	r := NewRegistry()
	v := NewVerifier(DefaultVerifierConfig(), r)

	result := v.VerifyReasoning("gpt-4o",
		&runtime.ModelResponse{
			Text:    "Based on the repository data, openai/openai-python has the most stars with 25,432. The top issues are about rate limiting and async support.",
			Latency: 2 * time.Second,
		},
		true, true, // tools were available and were called
	)

	if !result.Passed {
		t.Errorf("expected pass, got fail: %s", result.Reason)
	}
}

func TestVerifyReasoningHallucination(t *testing.T) {
	r := NewRegistry()
	v := NewVerifier(DefaultVerifierConfig(), r)

	result := v.VerifyReasoning("gpt-4o-mini",
		&runtime.ModelResponse{
			Text: "The most starred repo is openai-python with about 20,000 stars.",
		},
		true, false, // tools available but NOT called
	)

	if result.Passed {
		t.Error("expected fail for ungrounded response")
	}
	if !containsFlag(result.Flags, "ungrounded_response") {
		t.Error("expected 'ungrounded_response' flag")
	}

	// Check registry recorded the hallucination
	p := r.GetProfile("gpt-4o-mini")
	if p.HallucinationCount != 1 {
		t.Errorf("expected 1 hallucination in registry, got %d", p.HallucinationCount)
	}
}

func TestVerifyReasoningConfabulation(t *testing.T) {
	r := NewRegistry()
	v := NewVerifier(DefaultVerifierConfig(), r)

	result := v.VerifyReasoning("gpt-4o-mini",
		&runtime.ModelResponse{
			Text: "As an AI, I don't have access to real-time data, but based on my training data, I think the answer is...",
		},
		false, false,
	)

	if !containsFlag(result.Flags, "possible_confabulation") {
		t.Error("expected 'possible_confabulation' flag")
	}
}

func TestVerifyRepetitionDetection(t *testing.T) {
	r := NewRegistry()
	v := NewVerifier(DefaultVerifierConfig(), r)

	// Build a highly repetitive response
	repeated := ""
	for i := 0; i < 20; i++ {
		repeated += "The repository has many stars. The repository has many stars. "
	}

	result := v.VerifyReasoning("gpt-4o-mini",
		&runtime.ModelResponse{Text: repeated},
		false, false,
	)

	if !containsFlag(result.Flags, "repetitive_reasoning") {
		t.Error("expected 'repetitive_reasoning' flag")
	}
}

func TestVerifierEscalation(t *testing.T) {
	r := NewRegistry()
	v := NewVerifier(DefaultVerifierConfig(), r)

	failResult := VerifyResult{Verdict: VerdictFail, Passed: false}

	// First escalation should succeed
	if !v.ShouldEscalate("task-1", failResult) {
		t.Error("expected escalation on first failure")
	}

	// Second escalation should succeed (max is 2)
	if !v.ShouldEscalate("task-1", failResult) {
		t.Error("expected escalation on second failure")
	}

	// Third should be blocked
	if v.ShouldEscalate("task-1", failResult) {
		t.Error("expected escalation to be blocked after max")
	}

	// Different task should work
	if !v.ShouldEscalate("task-2", failResult) {
		t.Error("expected escalation for different task")
	}
}

func TestVerifierResetEscalations(t *testing.T) {
	r := NewRegistry()
	v := NewVerifier(DefaultVerifierConfig(), r)

	failResult := VerifyResult{Verdict: VerdictFail}
	v.ShouldEscalate("task-1", failResult)
	v.ShouldEscalate("task-1", failResult)

	v.ResetEscalations("task-1")

	if !v.ShouldEscalate("task-1", failResult) {
		t.Error("expected escalation after reset")
	}
}

func TestVerifierPassShouldNotEscalate(t *testing.T) {
	r := NewRegistry()
	v := NewVerifier(DefaultVerifierConfig(), r)

	passResult := VerifyResult{Verdict: VerdictPass, Passed: true}
	if v.ShouldEscalate("task-1", passResult) {
		t.Error("should not escalate on pass")
	}
}

func TestVerifierRegistryIntegration(t *testing.T) {
	r := NewRegistry()
	v := NewVerifier(DefaultVerifierConfig(), r)

	// Successful tool call
	v.VerifyToolCall("gpt-4o-mini", "github_list_repos",
		&runtime.ToolCall{Name: "github_list_repos", Result: `[{"name":"ark"}]`},
		&runtime.ModelResponse{Text: "called tool"},
	)

	// Failed tool call
	v.VerifyToolCall("gpt-4o-mini", "web_search",
		&runtime.ToolCall{Name: "", Result: ""},
		&runtime.ModelResponse{Text: ""},
	)

	p := r.GetProfile("gpt-4o-mini")
	if p == nil {
		t.Fatal("expected profile after verification")
	}

	// Should have recorded both observations
	if p.TotalCalls < 2 {
		t.Errorf("expected at least 2 calls in registry, got %d", p.TotalCalls)
	}
}

func TestFormatResult(t *testing.T) {
	vr := VerifyResult{
		Passed:     true,
		Verdict:    VerdictPass,
		Reason:     "all checks passed",
		Confidence: 0.95,
		Duration:   150 * time.Microsecond,
	}

	formatted := FormatResult(vr)
	if formatted == "" {
		t.Error("expected non-empty formatted result")
	}
	if !containsStr(formatted, "✓") {
		t.Error("expected checkmark in passing result")
	}
}

func TestRepetitionRatio(t *testing.T) {
	// Non-repetitive
	ratio := repetitionRatio("The quick brown fox jumps over the lazy dog near the river bank")
	if ratio > 0.1 {
		t.Errorf("expected low repetition ratio, got %.2f", ratio)
	}

	// Empty/short
	ratio = repetitionRatio("hi")
	if ratio != 0 {
		t.Errorf("expected 0 for short text, got %.2f", ratio)
	}
}

func TestHasConfabulationMarkers(t *testing.T) {
	if !hasConfabulationMarkers("As an AI, I cannot access real data") {
		t.Error("expected confabulation marker detected")
	}
	if hasConfabulationMarkers("The repository has 25,432 stars based on the API response") {
		t.Error("expected no confabulation marker")
	}
}

func containsStr(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && (s == substr || len(s) > len(substr) && findSubstring(s, substr))
}
func findSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
func containsFlag(flags []string, flag string) bool {
	for _, f := range flags {
		if f == flag {
			return true
		}
	}
	return false
}
