package main

import (
	"math"
	"testing"
)

// NOTE: kept alongside the uncommitted pkg/cost pricing WIP — it asserts the corrected
// (deterministic) price for a dated snapshot flowing through the ACTUAL session cost path
// (extSession.record -> cost.ModelPricing), not an isolated helper. It would fail on a clean
// main checkout, so it is not committed until the cost.go fix is.
func TestSessionCostUsesCanonicalPricingForSnapshotName(t *testing.T) {
	s := newExtSession(sessionCmd{Task: "live-example", Provider: "openai"})
	// the exact live run: OpenAI resolved gpt-4o-mini to this dated snapshot
	s.record(sessionCmd{Action: "model_call", Model: "gpt-4o-mini-2024-07-18",
		InputTokens: 72, OutputTokens: 22})
	run := runResultOf(t, s.finish(sessionCmd{Success: true}))

	d := run.Decisions[0]
	// telemetry PRESERVES the concrete resolved name...
	if d.Model != "gpt-4o-mini-2024-07-18" {
		t.Fatalf("reported model rewritten to %q; must preserve the concrete name", d.Model)
	}
	// ...while cost is derived at the CANONICAL gpt-4o-mini rate (0.15 in / 0.60 out $/M),
	// not gpt-4o's 2.5/10. 72*0.15/1e6 + 22*0.60/1e6 = 0.0000240.
	wantMini := 72*0.15/1e6 + 22*0.60/1e6
	wantGpt4o := 72*2.5/1e6 + 22*10.0/1e6
	if math.Abs(d.Cost.TotalCost-wantMini) > 1e-12 {
		t.Fatalf("snapshot priced at $%.8f; want mini $%.8f (gpt-4o would be $%.8f)",
			d.Cost.TotalCost, wantMini, wantGpt4o)
	}
	if math.Abs(run.TotalCost-d.Cost.TotalCost) > 1e-12 {
		t.Fatalf("Σ decision cost %.8f != total %.8f", d.Cost.TotalCost, run.TotalCost)
	}
	if run.CostByModel["gpt-4o-mini-2024-07-18"] != run.TotalCost {
		t.Fatalf("cost_by_model does not reconcile: %v", run.CostByModel)
	}
}
