package cost

import "testing"

// NOTE: this test validates the deterministic pricing resolution that lives in the
// (currently uncommitted) cost.go WIP. It is kept alongside that WIP — committing it while
// cost.go's fix is uncommitted would fail on a clean main checkout.

func inPerM(p TokenPrice) float64  { return p.InputPerToken * 1_000_000 }
func outPerM(p TokenPrice) float64 { return p.OutputPerToken * 1_000_000 }

func TestModelPricingResolution(t *testing.T) {
	cases := []struct {
		name         string
		provider     string
		model        string
		wantIn       float64
		wantOut      float64
		wantDisplay  string
	}{
		{"exact mini", "openai", "gpt-4o-mini", 0.15, 0.60, "GPT-4o Mini"},
		{"dated mini snapshot", "openai", "gpt-4o-mini-2024-07-18", 0.15, 0.60, "GPT-4o Mini"},
		{"exact gpt-4o", "openai", "gpt-4o", 2.50, 10.0, "GPT-4o"},
		{"dated gpt-4o snapshot", "openai", "gpt-4o-2024-08-06", 2.50, 10.0, "GPT-4o"},
		{"dated gpt-4o snapshot 2", "openai", "gpt-4o-2024-05-13", 2.50, 10.0, "GPT-4o"},
		// overlapping-prefix classes beyond OpenAI:
		{"ollama base", "ollama", "llama3", 0, 0, "Llama 3 (local)"},
		{"ollama tag exact", "ollama", "llama3.2:1b", 0, 0, "Llama 3.2 1B (local)"},
		{"ollama base variant", "ollama", "llama3-70b", 0, 0, "Llama 3 (local)"},
		// reverse base->snapshot (anthropic entry is stored dated):
		{"anthropic base -> snapshot", "anthropic", "claude-sonnet-4", 3.0, 15.0, "Claude Sonnet 4"},
		{"anthropic dated exact", "anthropic", "claude-sonnet-4-20250514", 3.0, 15.0, "Claude Sonnet 4"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := ModelPricing(c.provider, c.model)
			if inPerM(p) != c.wantIn || outPerM(p) != c.wantOut {
				t.Fatalf("%s/%s priced in=%.3f out=%.3f $/M, want in=%.3f out=%.3f",
					c.provider, c.model, inPerM(p), outPerM(p), c.wantIn, c.wantOut)
			}
			if c.wantDisplay != "" && p.DisplayName != c.wantDisplay {
				t.Fatalf("%s/%s display=%q want %q", c.provider, c.model, p.DisplayName, c.wantDisplay)
			}
		})
	}
}

// gpt-4o-mini snapshot must NOT resolve to gpt-4o (the exact live-run defect).
func TestSnapshotDoesNotMatchShorterSibling(t *testing.T) {
	p := ModelPricing("openai", "gpt-4o-mini-2024-07-18")
	if inPerM(p) != 0.15 {
		t.Fatalf("gpt-4o-mini snapshot mis-priced at in=%.3f $/M (want 0.15 — the mini rate, not gpt-4o's 2.5)", inPerM(p))
	}
	// and gpt-4o snapshot must not accidentally match the mini entry either
	q := ModelPricing("openai", "gpt-4o-2024-08-06")
	if inPerM(q) != 2.50 {
		t.Fatalf("gpt-4o snapshot mis-priced at in=%.3f $/M (want 2.5)", inPerM(q))
	}
}

func TestUnknownModelIsExplicitZero(t *testing.T) {
	p := ModelPricing("openai", "o1-preview")
	if inPerM(p) != 0 || outPerM(p) != 0 {
		t.Fatalf("unknown model should price at zero, got in=%.3f out=%.3f", inPerM(p), outPerM(p))
	}
	if p.DisplayName == "" || p.DisplayName == "GPT-4o" || p.DisplayName == "GPT-4o Mini" {
		t.Fatalf("unknown model should carry an explicit 'unknown pricing' label, got %q", p.DisplayName)
	}
	if _, ok := lookupPricing("openai", "o1-preview"); ok {
		t.Fatalf("lookupPricing should report ok=false for an unknown model")
	}
}

// A repeated lookup must return identical pricing every time — never dependent on Go map
// iteration order (the root cause of the live-run defect).
func TestRepeatedLookupIsDeterministic(t *testing.T) {
	pairs := []struct{ provider, model string }{
		{"openai", "gpt-4o-mini-2024-07-18"}, {"openai", "gpt-4o-2024-08-06"},
		{"ollama", "llama3.2:1b"}, {"anthropic", "claude-sonnet-4"}, {"openai", "o1-preview"},
	}
	for _, pr := range pairs {
		model := pr.model
		var first TokenPrice
		for i := 0; i < 1000; i++ {
			p := ModelPricing(pr.provider, model)
			if i == 0 {
				first = p
				continue
			}
			if p.InputPerToken != first.InputPerToken || p.OutputPerToken != first.OutputPerToken || p.DisplayName != first.DisplayName {
				t.Fatalf("%s: nondeterministic on iteration %d: %+v != %+v", model, i, p, first)
			}
		}
	}
}
