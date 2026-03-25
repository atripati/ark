package config




import (
	"os"
	"path/filepath"
	"testing"
)

func writeTestConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadBasicConfig(t *testing.T) {
	path := writeTestConfig(t, `
name: test-agent
version: "2.0"

model:
  provider: ollama
  name: llama3
  max_tokens: 2048

context:
  total_tokens: 100000
  strategy: static
  tool_budget: 15%
  memory_budget: 20%
  conversation_budget: 30%
  initial_tools: 5
  max_retries: 2

memory:
  backend: sqlite
  path: ./test.db
  shared: true

tracing:
  enabled: false
  output: file
  level: summary
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.Name != "test-agent" {
		t.Errorf("name: got %q, want %q", cfg.Name, "test-agent")
	}
	if cfg.Version != "2.0" {
		t.Errorf("version: got %q, want %q", cfg.Version, "2.0")
	}
	if cfg.Model.Provider != "ollama" {
		t.Errorf("provider: got %q, want %q", cfg.Model.Provider, "ollama")
	}
	if cfg.Model.Name != "llama3" {
		t.Errorf("model.name: got %q, want %q", cfg.Model.Name, "llama3")
	}
	if cfg.Model.MaxTokens != 2048 {
		t.Errorf("max_tokens: got %d, want %d", cfg.Model.MaxTokens, 2048)
	}
	if cfg.Context.TotalTokens != 100000 {
		t.Errorf("total_tokens: got %d, want %d", cfg.Context.TotalTokens, 100000)
	}
	if cfg.Context.Strategy != "static" {
		t.Errorf("strategy: got %q, want %q", cfg.Context.Strategy, "static")
	}
	if cfg.Context.ToolBudgetPct != 15 {
		t.Errorf("tool_budget: got %d, want %d", cfg.Context.ToolBudgetPct, 15)
	}
	if cfg.Context.MemoryBudgetPct != 20 {
		t.Errorf("memory_budget: got %d, want %d", cfg.Context.MemoryBudgetPct, 20)
	}
	if cfg.Context.ConversationBudgetPct != 30 {
		t.Errorf("conversation_budget: got %d, want %d", cfg.Context.ConversationBudgetPct, 30)
	}
	if cfg.Context.InitialTools != 5 {
		t.Errorf("initial_tools: got %d, want %d", cfg.Context.InitialTools, 5)
	}
	if cfg.Context.MaxRetries != 2 {
		t.Errorf("max_retries: got %d, want %d", cfg.Context.MaxRetries, 2)
	}
	if cfg.Memory.Backend != "sqlite" {
		t.Errorf("backend: got %q, want %q", cfg.Memory.Backend, "sqlite")
	}
	if !cfg.Memory.Shared {
		t.Error("shared: got false, want true")
	}
	if cfg.Tracing.Enabled {
		t.Error("tracing.enabled: got true, want false")
	}
	if cfg.Tracing.Level != "summary" {
		t.Errorf("tracing.level: got %q, want %q", cfg.Tracing.Level, "summary")
	}
}

func TestLoadInlineComments(t *testing.T) {
	path := writeTestConfig(t, `
name: my-agent  # this is my agent
model:
  provider: anthropic  # anthropic | openai | ollama
  name: claude-sonnet-4-20250514
  max_tokens: 4096
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Comments should be stripped — provider should be "anthropic", not "anthropic  # ..."
	if cfg.Model.Provider != "anthropic" {
		t.Errorf("provider with inline comment: got %q, want %q", cfg.Model.Provider, "anthropic")
	}
	if cfg.Name != "my-agent" {
		t.Errorf("name with inline comment: got %q, want %q", cfg.Name, "my-agent")
	}
}

func TestLoadQuotedStrings(t *testing.T) {
	path := writeTestConfig(t, `
name: "quoted-agent"
version: '1.0'
model:
  provider: ollama
  name: "llama3"
  base_url: "http://custom:11434"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.Name != "quoted-agent" {
		t.Errorf("double-quoted name: got %q, want %q", cfg.Name, "quoted-agent")
	}
	if cfg.Version != "1.0" {
		t.Errorf("single-quoted version: got %q, want %q", cfg.Version, "1.0")
	}
	if cfg.Model.BaseURL != "http://custom:11434" {
		t.Errorf("quoted base_url: got %q, want %q", cfg.Model.BaseURL, "http://custom:11434")
	}
}

func TestLoadMultipleTools(t *testing.T) {
	path := writeTestConfig(t, `
name: multi-tool
model:
  provider: ollama
  name: llama3

tools:
  - name: github
    type: mcp
    uri: "https://github.mcp.example.com"
    description: "GitHub integration"
  - name: slack
    type: mcp
    uri: "https://slack.mcp.example.com"
  - name: search
    type: function
    description: "Web search"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if len(cfg.Tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(cfg.Tools))
	}

	if cfg.Tools[0].Name != "github" || cfg.Tools[0].Type != "mcp" {
		t.Errorf("tool 0: got %+v", cfg.Tools[0])
	}
	if cfg.Tools[0].Description != "GitHub integration" {
		t.Errorf("tool 0 description: got %q", cfg.Tools[0].Description)
	}
	if cfg.Tools[1].Name != "slack" {
		t.Errorf("tool 1: got %q, want %q", cfg.Tools[1].Name, "slack")
	}
	if cfg.Tools[2].Name != "search" || cfg.Tools[2].Type != "function" {
		t.Errorf("tool 2: got %+v", cfg.Tools[2])
	}
}

func TestLoadDefaultsApplied(t *testing.T) {
	// Minimal config — everything else should get defaults
	path := writeTestConfig(t, `
name: minimal
model:
  provider: ollama
  name: llama3
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.Context.TotalTokens != 200000 {
		t.Errorf("default total_tokens: got %d, want %d", cfg.Context.TotalTokens, 200000)
	}
	if cfg.Context.Strategy != "adaptive" {
		t.Errorf("default strategy: got %q, want %q", cfg.Context.Strategy, "adaptive")
	}
	if cfg.Context.InitialTools != 3 {
		t.Errorf("default initial_tools: got %d, want %d", cfg.Context.InitialTools, 3)
	}
	if cfg.Model.MaxTokens != 4096 {
		t.Errorf("default max_tokens: got %d, want %d", cfg.Model.MaxTokens, 4096)
	}
	if cfg.Tracing.Enabled != true {
		t.Error("default tracing.enabled: got false, want true")
	}
}

func TestValidateInvalidProvider(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Model.Provider = "bad_provider"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for bad provider")
	}
	if got := err.Error(); !contains(got, "bad_provider") {
		t.Errorf("error should mention bad provider, got: %s", got)
	}
}

func TestValidateEmptyModelName(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Model.Name = ""
	cfg.Model.APIKey = "test-key"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for empty model name")
	}
	if got := err.Error(); !contains(got, "model.name") {
		t.Errorf("error should mention model.name, got: %s", got)
	}
}

func TestValidateMissingAPIKey(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Model.Provider = "anthropic"
	cfg.Model.APIKey = ""
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for missing API key")
	}
	if got := err.Error(); !contains(got, "ANTHROPIC_API_KEY") {
		t.Errorf("error should mention env var, got: %s", got)
	}
}

func TestValidateOllamaNoKeyNeeded(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Model.Provider = "ollama"
	cfg.Model.Name = "llama3"
	cfg.Model.APIKey = ""
	err := cfg.Validate()
	if err != nil {
		t.Errorf("ollama should not require API key, got: %v", err)
	}
}

func TestValidateBudgetExceedsLimit(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Model.Provider = "ollama"
	cfg.Model.Name = "llama3"
	cfg.Context.ToolBudgetPct = 40
	cfg.Context.MemoryBudgetPct = 30
	cfg.Context.ConversationBudgetPct = 25

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for budget exceeding 90%")
	}
	if got := err.Error(); !contains(got, "95%") {
		t.Errorf("error should show total budget, got: %s", got)
	}
}

func TestValidateBudgetWithinLimit(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Model.Provider = "ollama"
	cfg.Model.Name = "llama3"
	cfg.Context.ToolBudgetPct = 10
	cfg.Context.MemoryBudgetPct = 10
	cfg.Context.ConversationBudgetPct = 35

	err := cfg.Validate()
	if err != nil {
		t.Errorf("budget at 55%% should pass, got: %v", err)
	}
}

func TestValidateMultipleErrors(t *testing.T) {
	cfg := Config{
		Model: ModelConfig{
			Provider:  "invalid",
			Name:      "",
			MaxTokens: -1,
		},
		Context: ContextConfig{
			TotalTokens: 0,
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation errors")
	}

	got := err.Error()
	if !contains(got, "problems") {
		t.Errorf("multiple errors should say 'problems', got: %s", got)
	}
}

func TestStripCommentQuoteAware(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`value # comment`, `value `},
		{`"value # not a comment"`, `"value # not a comment"`},
		{`'value # not a comment'`, `'value # not a comment'`},
		{`plain`, `plain`},
		{`# full line comment`, ``},
	}

	for _, tt := range tests {
		got := stripComment(tt.input)
		if got != tt.want {
			t.Errorf("stripComment(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFileNotFound(t *testing.T) {
	_, err := Load("/nonexistent/agent.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

// helper
func contains(s, substr string) bool {
	return len(s) >= len(substr) && findSubstring(s, substr)
}

func findSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}