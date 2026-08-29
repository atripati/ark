package cost

import (
	"strings"
	"testing"
	"time"
)

// The core bug this guards against: pricing every step at the configured model
// even when per-step routing sent the work to a cheaper model. That overstated
// cost (16.7x on a real run) and hid the savings routing actually produced.
func TestRecordStepWithModel_PricesPerStep(t *testing.T) {
	tracker := NewTracker(ModelPricing("openai", "gpt-4o"))

	// This step actually ran on the cheap model.
	sc := tracker.RecordStepWithModel(1, 452, 16, "tool_call", "github_search", time.Second, "openai", "gpt-4o-mini")

	wantIn := 452 * (0.15 / 1_000_000)
	wantOut := 16 * (0.60 / 1_000_000)
	want := wantIn + wantOut

	if diff := sc.TotalCost - want; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("step priced at %.10f, want %.10f (should use gpt-4o-mini rates)", sc.TotalCost, want)
	}

	// Sanity: pricing it at the configured model would be dramatically higher.
	atConfigured := 452*(2.50/1_000_000) + 16*(10.0/1_000_000)
	if sc.TotalCost >= atConfigured {
		t.Errorf("cheap-model step should cost far less than configured model (%.10f vs %.10f)", sc.TotalCost, atConfigured)
	}
}

// An empty model must fall back to the tracker's configured pricing, so the
// old call path keeps working unchanged.
func TestRecordStepWithModel_EmptyModelFallsBack(t *testing.T) {
	tracker := NewTracker(ModelPricing("openai", "gpt-4o"))
	sc := tracker.RecordStepWithModel(1, 1000, 100, "complete", "", time.Second, "openai", "")

	want := 1000*(2.50/1_000_000) + 100*(10.0/1_000_000)
	if diff := sc.TotalCost - want; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("empty model should fall back to configured pricing: got %.10f want %.10f", sc.TotalCost, want)
	}
}

// RecordStep must remain backward compatible.
func TestRecordStep_BackwardCompatible(t *testing.T) {
	tracker := NewTracker(ModelPricing("openai", "gpt-4o"))
	sc := tracker.RecordStep(1, 1000, 100, "complete", "", time.Second)

	want := 1000*(2.50/1_000_000) + 100*(10.0/1_000_000)
	if diff := sc.TotalCost - want; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("RecordStep should price at configured model: got %.10f want %.10f", sc.TotalCost, want)
	}
}

// A mixed-model task must report every model it used, not just the configured one.
func TestReport_LabelsAllModelsUsed(t *testing.T) {
	tracker := NewTracker(ModelPricing("openai", "gpt-4o"))
	tracker.RecordStepWithModel(1, 452, 16, "tool_call", "t", time.Second, "openai", "gpt-4o-mini")
	tracker.RecordStepWithModel(2, 885, 129, "complete", "", time.Second, "openai", "gpt-4o")

	rep := tracker.Report()
	if !strings.Contains(rep.Model, "Mini") || !strings.Contains(rep.Model, "GPT-4o") {
		t.Errorf("report should name both models, got %q", rep.Model)
	}
	if !strings.Contains(rep.Model, "routed") {
		t.Errorf("mixed-model report should be marked as routed, got %q", rep.Model)
	}
}

// A single-model task should report just that model, with no "routed" suffix.
func TestReport_SingleModelLabel(t *testing.T) {
	tracker := NewTracker(ModelPricing("openai", "gpt-4o"))
	tracker.RecordStepWithModel(1, 100, 10, "complete", "", time.Second, "openai", "gpt-4o")

	rep := tracker.Report()
	if strings.Contains(rep.Model, "routed") {
		t.Errorf("single-model report should not say routed, got %q", rep.Model)
	}
}

// The total must be the sum of per-step costs, each at its own model's rate.
func TestReport_TotalIsSumOfPerStepPricing(t *testing.T) {
	tracker := NewTracker(ModelPricing("openai", "gpt-4o"))
	s1 := tracker.RecordStepWithModel(1, 452, 16, "tool_call", "t", time.Second, "openai", "gpt-4o-mini")
	s2 := tracker.RecordStepWithModel(2, 885, 129, "complete", "", time.Second, "openai", "gpt-4o")

	rep := tracker.Report()
	want := s1.TotalCost + s2.TotalCost
	if diff := rep.TotalCost - want; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("total %.10f should equal sum of steps %.10f", rep.TotalCost, want)
	}
}

// A report for a run where no model was called must not name the configured
// model. Doing so asserts a call that never happened — the same class of false
// claim as pricing a routed step at the wrong model's rate.
func TestReport_NoStepsNamesNoModel(t *testing.T) {
	tracker := NewTracker(ModelPricing("openai", "gpt-4o"))

	rep := tracker.Report()
	if rep.Model != "none" {
		t.Errorf("Model = %q, want %q — no model ran, so none may be named", rep.Model, "none")
	}
	if rep.TotalCost != 0 {
		t.Errorf("TotalCost = %f, want 0", rep.TotalCost)
	}
}

// Steps that consumed tokens without recording a model still fall back to the
// configured model, which is honest: something was called, and that is the best
// available attribution.
func TestReport_StepsWithoutModelFallBackToConfigured(t *testing.T) {
	tracker := NewTracker(ModelPricing("openai", "gpt-4o"))
	tracker.RecordStep(1, 100, 10, "complete", "", time.Second)

	rep := tracker.Report()
	if rep.Model == "none" {
		t.Error("a step did run, so the configured model is the honest attribution")
	}
}

// The case that actually occurs: a run refused before any model call still
// records one step explaining the refusal. Step count is therefore not evidence
// that a model ran — token usage is. An earlier version of this check tested
// zero steps, passed, and still printed the configured model in production.
func TestReport_ZeroTokenStepNamesNoModel(t *testing.T) {
	tracker := NewTracker(ModelPricing("openai", "gpt-4o"))
	tracker.RecordStepWithModel(1, 0, 0, "ungroundable", "", 0, "openai", "")

	rep := tracker.Report()
	if rep.Model != "none" {
		t.Errorf("Model = %q, want %q — the step consumed no tokens, so no model was called", rep.Model, "none")
	}
}
