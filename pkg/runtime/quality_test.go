package runtime

import (
	"strings"
	"testing"
)

func TestOptimizePromptCoding(t *testing.T) {
	result := OptimizePrompt("write a function to parse CSV in Go", "coding")
	if !strings.Contains(result, "Quality constraints") {
		t.Error("expected quality constraints for coding task")
	}
	if !strings.Contains(result, "simplest correct approach") {
		t.Error("expected simplicity constraint for coding")
	}
}

func TestOptimizePromptPrecise(t *testing.T) {
	// Already precise prompts should not be modified
	precise := "Return only the JSON output, do not include explanations"
	result := OptimizePrompt(precise, "coding")
	if result != precise {
		t.Errorf("precise prompt should not be modified, got: %s", result)
	}
}

func TestOptimizePromptRanking(t *testing.T) {
	result := OptimizePrompt("find the top 5 JavaScript frameworks", "ranking")
	if !strings.Contains(result, "numbered list") {
		t.Error("expected numbered list constraint for ranking")
	}
}

func TestStripBoilerplateStart(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"Sure! Here is the code.", "Here is the code."},
		{"Of course! Let me explain.", "Let me explain."},
		{"I'd be happy to help! The answer is 42.", "The answer is 42."},
		{"Certainly, here you go.", "Here you go."},
		{"The answer is 42.", "The answer is 42."},
	}

	for _, c := range cases {
		result := stripBoilerplate(c.input)
		if result != c.expected {
			t.Errorf("stripBoilerplate(%q) = %q, want %q", c.input, result, c.expected)
		}
	}
}

func TestStripBoilerplateEnd(t *testing.T) {
	input := "The answer is 42.\n\nLet me know if you have any questions!"
	result := stripBoilerplate(input)
	if strings.Contains(result, "Let me know") {
		t.Errorf("should strip trailing boilerplate, got: %s", result)
	}
}

func TestCleanResponseCoding(t *testing.T) {
	config := DefaultQualityConfig()
	input := "Sure! Here is the code:\n```go\nfunc main() {}\n```\n\nLet me know if you have any questions!"
	result := CleanResponse(input, "coding", config)
	if strings.Contains(result, "Sure!") {
		t.Error("should strip 'Sure!' prefix")
	}
	if strings.Contains(result, "Let me know") {
		t.Error("should strip 'Let me know' suffix")
	}
	if !strings.Contains(result, "func main()") {
		t.Error("should preserve code content")
	}
}

func TestCleanResponseSummary(t *testing.T) {
	config := DefaultQualityConfig()
	input := "In summary, the main point is X."
	result := CleanResponse(input, "summarization", config)
	if strings.HasPrefix(result, "In summary") {
		t.Error("should strip 'In summary' from summary responses")
	}
}

func TestCleanResponseReasoning(t *testing.T) {
	config := DefaultQualityConfig()
	input := "This is a complex topic, but the answer is clear."
	result := CleanResponse(input, "reasoning", config)
	if strings.Contains(result, "complex topic") {
		t.Error("should strip hedging from reasoning responses")
	}
}

func TestDeduplicateResponse(t *testing.T) {
	input := "The answer is 42.\nSome other text.\nThe answer is 42."
	result := DeduplicateResponse(input)
	count := strings.Count(result, "The answer is 42.")
	if count != 1 {
		t.Errorf("expected 1 occurrence, got %d", count)
	}
}

func TestDeduplicatePreservesShortLines(t *testing.T) {
	input := "1. Item\n2. Item\n3. Item"
	result := DeduplicateResponse(input)
	if result != input {
		t.Errorf("should preserve short lines, got: %s", result)
	}
}

func TestValidateCodeResponse(t *testing.T) {
	input := "```go\npackage main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n```"
	q := ValidateCodeResponse(input)
	if !q.HasCode {
		t.Error("should detect code")
	}
	if !q.HasImports {
		t.Error("should detect imports")
	}
	if !q.IsComplete {
		t.Error("should detect complete code")
	}
	if q.LinesOfCode < 3 {
		t.Errorf("expected at least 3 lines of code, got %d", q.LinesOfCode)
	}
}

func TestValidateCodeResponseNoCode(t *testing.T) {
	q := ValidateCodeResponse("This is just text, no code here.")
	if q.HasCode {
		t.Error("should not detect code in plain text")
	}
}

func TestEnhanceSystemPromptCoding(t *testing.T) {
	base := "You are an assistant."
	result := EnhanceSystemPrompt(base, "coding")
	if !strings.Contains(result, "minimal, correct code") {
		t.Error("should add coding directives")
	}
}

func TestEnhanceSystemPromptDefault(t *testing.T) {
	base := "You are an assistant."
	result := EnhanceSystemPrompt(base, "general")
	if !strings.Contains(result, "Be direct") {
		t.Error("should add default directives")
	}
	if !strings.Contains(result, "Do NOT start with") {
		t.Error("should include anti-boilerplate directive")
	}
}

func TestCleanResponsePreservesContent(t *testing.T) {
	config := DefaultQualityConfig()
	// Technical content should not be stripped
	input := "The function uses a hash map for O(1) lookups. Here is the implementation:\n```python\ndef lookup(key):\n    return cache.get(key)\n```"
	result := CleanResponse(input, "coding", config)
	if !strings.Contains(result, "hash map") {
		t.Error("should preserve technical content")
	}
	if !strings.Contains(result, "def lookup") {
		t.Error("should preserve code")
	}
}

func TestCleanResponseEmpty(t *testing.T) {
	config := DefaultQualityConfig()
	result := CleanResponse("Sure! ", "general", config)
	// Should not return empty — falls back to original
	if result == "" {
		t.Error("should not return empty string")
	}
}
