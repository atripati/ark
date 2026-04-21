package governor

import (
	"testing"
)

func TestClassifyTaskRetrieval(t *testing.T) {
	cases := []string{
		"find the most starred repo",
		"list my github issues",
		"get the current price",
		"search for recent commits",
		"show me the latest release",
	}
	for _, q := range cases {
		tt := ClassifyTask(q)
		if tt != TaskRetrieval {
			t.Errorf("expected retrieval for %q, got %s", q, tt)
		}
	}
}

func TestClassifyTaskReasoning(t *testing.T) {
	cases := []string{
		"why is this failing",
		"explain how the routing works",
		"analyze the cost breakdown",
		"which is better for this use case",
		"should i use gpt-4o or claude",
	}
	for _, q := range cases {
		tt := ClassifyTask(q)
		if tt != TaskReasoning {
			t.Errorf("expected reasoning for %q, got %s", q, tt)
		}
	}
}

func TestClassifyTaskSummarization(t *testing.T) {
	cases := []string{
		"summarize the results",
		"give me a brief overview",
		"tldr of this document",
		"what are the key points",
	}
	for _, q := range cases {
		tt := ClassifyTask(q)
		if tt != TaskSummarization {
			t.Errorf("expected summarization for %q, got %s", q, tt)
		}
	}
}

func TestClassifyTaskCoding(t *testing.T) {
	cases := []string{
		"write a function to sort the list",
		"debug this code",
		"implement a binary search algorithm",
		"fix the bug in the api endpoint",
	}
	for _, q := range cases {
		tt := ClassifyTask(q)
		if tt != TaskCoding {
			t.Errorf("expected coding for %q, got %s", q, tt)
		}
	}
}

func TestClassifyTaskMultiStep(t *testing.T) {
	cases := []string{
		"first find the repo and then list its issues",
		"search for the user, and then compare their repos, and finally summarize",
		"find the repo, extract stars, fetch issues, and summarize the results",
	}
	for _, q := range cases {
		tt := ClassifyTask(q)
		if tt != TaskMultiStep {
			t.Errorf("expected multi_step for %q, got %s", q, tt)
		}
	}
}

func TestClassifyTaskGeneral(t *testing.T) {
	tt := ClassifyTask("hello there")
	if tt != TaskGeneral {
		t.Errorf("expected general for generic input, got %s", tt)
	}
}

func TestPredictFailureNoHistory(t *testing.T) {
	r := NewRegistry()
	pred := r.PredictFailure("gpt-4o-mini", TaskRetrieval, "github_list_repos")
	if pred.ShouldAvoid {
		t.Error("should not avoid model with no history")
	}
	if pred.Risk != 0.0 {
		t.Errorf("expected 0 risk, got %.2f", pred.Risk)
	}
}

func TestPredictFailureHighRisk(t *testing.T) {
	r := NewRegistry()

	// Create a model with bad tool history
	for i := 0; i < 5; i++ {
		r.Record(Observation{Model: "gpt-4o-mini", Kind: ObsToolCallFailure, ToolName: "web_search", StepType: "tool_call", ErrorClass: "timeout"})
	}
	r.Record(Observation{Model: "gpt-4o-mini", Kind: ObsToolCallSuccess, ToolName: "web_search", StepType: "tool_call"})

	pred := r.PredictFailure("gpt-4o-mini", TaskRetrieval, "web_search")
	if !pred.ShouldAvoid {
		t.Error("should avoid model with 83% failure rate on this tool")
	}
	if pred.Risk < 0.5 {
		t.Errorf("expected risk >= 0.5, got %.2f", pred.Risk)
	}
}

func TestPredictFailureSuggestsAlternative(t *testing.T) {
	r := NewRegistry()

	// gpt-4o-mini: bad at web_search
	for i := 0; i < 5; i++ {
		r.Record(Observation{Model: "gpt-4o-mini", Kind: ObsToolCallFailure, ToolName: "web_search", StepType: "tool_call"})
	}

	// gpt-4o: good at web_search
	for i := 0; i < 5; i++ {
		r.Record(Observation{Model: "gpt-4o", Kind: ObsToolCallSuccess, ToolName: "web_search", StepType: "tool_call"})
	}

	pred := r.PredictFailure("gpt-4o-mini", TaskRetrieval, "web_search")
	if pred.Alternative != "gpt-4o" {
		t.Errorf("expected gpt-4o as alternative, got %q", pred.Alternative)
	}
}

func TestPredictFailureHallucinationRisk(t *testing.T) {
	r := NewRegistry()

	// Create high hallucination rate
	for i := 0; i < 3; i++ {
		r.Record(Observation{Model: "gpt-4o-mini", Kind: ObsToolCallSuccess, StepType: "tool_call"})
	}
	for i := 0; i < 2; i++ {
		r.Record(Observation{Model: "gpt-4o-mini", Kind: ObsHallucination, StepType: "complete"})
	}

	pred := r.PredictFailure("gpt-4o-mini", TaskReasoning, "")
	if pred.Risk < 0.1 {
		t.Errorf("expected some risk from hallucination rate, got %.2f", pred.Risk)
	}
}

func TestEffortLowForRetrieval(t *testing.T) {
	plan := DetermineEffort(TaskRetrieval, 1.0, FailurePrediction{Risk: 0})
	if plan.Level != EffortLow {
		t.Errorf("expected low effort for simple retrieval, got %s", plan.Level)
	}
	if plan.PreferredTier != "fast" {
		t.Errorf("expected fast tier, got %s", plan.PreferredTier)
	}
	if plan.VerifyOutput {
		t.Error("should not verify on low effort")
	}
}

func TestEffortHighForMultiStep(t *testing.T) {
	plan := DetermineEffort(TaskMultiStep, 1.0, FailurePrediction{Risk: 0})
	if plan.Level != EffortHigh {
		t.Errorf("expected high effort for multi-step, got %s", plan.Level)
	}
	if plan.PreferredTier != "strong" {
		t.Errorf("expected strong tier, got %s", plan.PreferredTier)
	}
	if !plan.VerifyOutput {
		t.Error("should verify on high effort")
	}
	if !plan.AllowDisagree {
		t.Error("should allow disagreement check on high effort")
	}
}

func TestEffortHighForHighRisk(t *testing.T) {
	plan := DetermineEffort(TaskRetrieval, 1.0, FailurePrediction{Risk: 0.7})
	if plan.Level != EffortHigh {
		t.Errorf("expected high effort for high risk, got %s", plan.Level)
	}
}

func TestEffortMediumForLowConfidence(t *testing.T) {
	plan := DetermineEffort(TaskRetrieval, 0.4, FailurePrediction{Risk: 0})
	if plan.Level != EffortMedium {
		t.Errorf("expected medium effort for low confidence, got %s", plan.Level)
	}
}

func TestEffortMediumForReasoning(t *testing.T) {
	plan := DetermineEffort(TaskReasoning, 1.0, FailurePrediction{Risk: 0})
	if plan.Level != EffortMedium {
		t.Errorf("expected medium effort for reasoning, got %s", plan.Level)
	}
}

func TestEffortMediumForCoding(t *testing.T) {
	plan := DetermineEffort(TaskCoding, 1.0, FailurePrediction{Risk: 0})
	if plan.Level != EffortMedium {
		t.Errorf("expected medium effort for coding, got %s", plan.Level)
	}
}

func TestExperienceContextEmpty(t *testing.T) {
	r := NewRegistry()
	ctx := r.BuildExperienceContext("gpt-4o-mini", "github_list_repos")
	if ctx != "" {
		t.Errorf("expected empty context for unknown model, got %q", ctx)
	}
}

func TestExperienceContextWithFailures(t *testing.T) {
	r := NewRegistry()

	// Record failures
	for i := 0; i < 5; i++ {
		r.Record(Observation{Model: "gpt-4o-mini", Kind: ObsToolCallFailure, ToolName: "web_search", ErrorClass: "invalid_json"})
	}
	r.Record(Observation{Model: "gpt-4o-mini", Kind: ObsToolCallSuccess, ToolName: "web_search"})

	ctx := r.BuildExperienceContext("gpt-4o-mini", "web_search")
	if ctx == "" {
		t.Error("expected non-empty experience context for model with failures")
	}
	if !containsStr(ctx, "Governor Notes") {
		t.Error("expected Governor Notes in context")
	}
}

func TestExperienceContextHallucination(t *testing.T) {
	r := NewRegistry()

	for i := 0; i < 5; i++ {
		r.Record(Observation{Model: "gpt-4o-mini", Kind: ObsToolCallSuccess, StepType: "tool_call"})
	}
	r.Record(Observation{Model: "gpt-4o-mini", Kind: ObsHallucination, StepType: "complete"})

	ctx := r.BuildExperienceContext("gpt-4o-mini", "")
	if ctx == "" {
		t.Error("expected experience context for model with hallucination history")
	}
	if !containsStr(ctx, "tool") {
		t.Error("expected hint about using tools")
	}
}

func TestDisagreementSimilarOutputs(t *testing.T) {
	output1 := "The most starred repository is openai-python with 25,432 stars. The top issues are about rate limiting."
	output2 := "openai-python has the most stars at 25,432. The main issues relate to rate limiting and async support."

	result := CheckDisagreement(output1, output2)
	if !result.Agree {
		t.Errorf("expected agreement, got disagreement (similarity: %.2f)", result.Similarity)
	}
}

func TestDisagreementDifferentOutputs(t *testing.T) {
	output1 := "The weather in Chicago is sunny and warm today with a high of 75 degrees."
	output2 := "PostgreSQL query optimization requires proper indexing on join columns and WHERE clause predicates."

	result := CheckDisagreement(output1, output2)
	if result.Agree {
		t.Errorf("expected disagreement for completely different outputs (similarity: %.2f)", result.Similarity)
	}
}

func TestDisagreementEmptyOutput(t *testing.T) {
	result := CheckDisagreement("", "some output")
	if result.Agree {
		t.Error("expected disagreement for empty output")
	}
}

func TestDisagreementIdentical(t *testing.T) {
	output := "The repository has 100 stars and 5 open issues."
	result := CheckDisagreement(output, output)
	if !result.Agree {
		t.Error("expected agreement for identical outputs")
	}
	if result.Similarity != 1.0 {
		t.Errorf("expected 1.0 similarity for identical, got %.2f", result.Similarity)
	}
}
