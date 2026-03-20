package context

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestDefaultBudget(t *testing.T) {
	b := DefaultBudget(200000)

	total := b.System + b.Tools + b.Memory + b.Conversation + b.Working + b.Reserved
	if total != b.Total {
		t.Errorf("budget allocations (%d) should equal total (%d)", total, b.Total)
	}

	// Tools should be 10%, not the 30-50% that raw MCP uses
	toolPct := float64(b.Tools) / float64(b.Total) * 100
	if toolPct > 15 {
		t.Errorf("tool budget should be ≤15%% of total, got %.1f%%", toolPct)
	}
}

func TestRegisterAndLoad(t *testing.T) {
	m := NewManager(DefaultBudget(200000))

	block := &ContextBlock{
		ID:         "tool-github",
		Type:       BlockTool,
		Content:    "Full GitHub tool schema with 20 endpoints...",
		TokenCount: 500,
		Priority:   0.5,
		LastUsed:   time.Now(),
		Tags:       []string{"github", "code", "repository"},
	}

	m.Register(block)

	if err := m.Load("tool-github"); err != nil {
		t.Fatalf("failed to load block: %v", err)
	}

	active := m.ActiveBlocks()
	if len(active) != 1 || active[0] != "tool-github" {
		t.Errorf("expected 1 active block 'tool-github', got %v", active)
	}
}

func TestCompressionSavesTokens(t *testing.T) {
	m := NewManager(DefaultBudget(200000))
	fullSchema := strings.Repeat(`{"name":"create_issue","description":"Creates a new issue in a GitHub repository","parameters":{"type":"object","properties":{"repo":{"type":"string"},"title":{"type":"string"},"body":{"type":"string"}}}}`, 5)

	block := m.RegisterTool("tool-github-issues", "create_issue",
		"Creates a new issue in a GitHub repository with labels and assignees",
		fullSchema)

	if block.CompressedTokens >= block.TokenCount {
		t.Errorf("compressed (%d) should be smaller than full (%d)",
			block.CompressedTokens, block.TokenCount)
	}

	ratio := float64(block.CompressedTokens) / float64(block.TokenCount) * 100
	t.Logf("Compression ratio: %.1f%% (full=%d, compressed=%d tokens)",
		ratio, block.TokenCount, block.CompressedTokens)
}

func TestTokenBloatSimulation(t *testing.T) {

	m := NewManager(DefaultBudget(200000))

	servers := []string{"github", "slack", "jira", "gmail", "drive", "calendar", "database"}
	totalRawTokens := 0
	toolCount := 0

	for _, server := range servers {
		for i := 0; i < 30; i++ {
			name := fmt.Sprintf("%s_tool_%d", server, i)
			schema := fmt.Sprintf(`{"name":"%s","description":"Tool %d for %s server with full parameter schema including types, defaults, constraints, and examples","inputSchema":{"type":"object","properties":{"param1":{"type":"string","description":"First parameter with detailed description"},"param2":{"type":"integer","description":"Second parameter"},"param3":{"type":"boolean","default":false}},"required":["param1"]}}`, name, i, server)

			block := m.RegisterTool(
				fmt.Sprintf("%s-%d", server, i),
				name,
				fmt.Sprintf("Tool %d for %s operations including CRUD and search", i, server),
				schema,
			)
			totalRawTokens += block.TokenCount
			toolCount++
		}
	}
	rawPct := float64(totalRawTokens) / float64(200000) * 100

	loaded := m.LoadRelevant("create a github issue for the bug", 10)

	stats := m.Stats()
	usage := m.Usage()

	t.Logf("\n=== ARK Token Bloat Comparison ===")
	t.Logf("Total tools registered: %d", toolCount)
	t.Logf("Raw MCP approach: %d tokens (%.1f%% of context window)", totalRawTokens, rawPct)
	t.Logf("ARK approach: loaded %d relevant tools", len(loaded))
	t.Logf("Tokens saved: %d", stats.TokensSaved)
	t.Logf("\n%s", usage)
	if rawPct < 20 {
		t.Logf("Warning: raw token usage lower than expected (%.1f%%)", rawPct)
	}

	if len(loaded) > 10 {
		t.Errorf("should load at most 10 tools, loaded %d", len(loaded))
	}
}

func TestEvictionByPriority(t *testing.T) {
	budget := TokenBudget{
		Total:        1000,
		Tools:        200,
		System:       100,
		Memory:       200,
		Conversation: 300,
		Working:      100,
		Reserved:     100,
	}
	m := NewManager(budget)

	m.Register(&ContextBlock{
		ID: "low-priority", Type: BlockTool,
		Content: "Low priority tool", TokenCount: 150,
		Priority: 0.1, LastUsed: time.Now().Add(-10 * time.Minute),
		Tags: []string{"low"},
	})
	m.Register(&ContextBlock{
		ID: "high-priority", Type: BlockTool,
		Content: "High priority tool", TokenCount: 150,
		Priority: 0.9, LastUsed: time.Now(),
		Tags: []string{"high"},
	})

	if err := m.Load("low-priority"); err != nil {
		t.Fatal(err)
	}

	if err := m.Load("high-priority"); err != nil {
		t.Fatal(err)
	}

	stats := m.Stats()
	if stats.TotalEvictions != 1 {
		t.Errorf("expected 1 eviction, got %d", stats.TotalEvictions)
	}

	active := m.ActiveBlocks()
	if len(active) != 1 || active[0] != "high-priority" {
		t.Errorf("expected only high-priority to be active, got %v", active)
	}
}

func TestRelevanceLoading(t *testing.T) {
	m := NewManager(DefaultBudget(200000))

	// Register tools from different domains
	m.RegisterTool("gh-1", "github_create_pr", "Create a pull request on GitHub", "{}")
	m.RegisterTool("gh-2", "github_list_issues", "List issues in a GitHub repository", "{}")
	m.RegisterTool("sl-1", "slack_send_message", "Send a message to a Slack channel", "{}")
	m.RegisterTool("sl-2", "slack_list_channels", "List all Slack channels", "{}")
	m.RegisterTool("db-1", "database_query", "Execute a SQL query on the database", "{}")

	loaded := m.LoadRelevant("create a pull request on github", 3)

	hasGithub := false
	hasSlack := false
	for _, id := range loaded {
		if strings.HasPrefix(id, "gh-") {
			hasGithub = true
		}
		if strings.HasPrefix(id, "sl-") {
			hasSlack = true
		}
	}

	if !hasGithub {
		t.Error("should have loaded GitHub tools for a GitHub query")
	}
	if hasSlack {
		t.Error("should NOT have loaded Slack tools for a GitHub query")
	}
}

func TestRenderOrdering(t *testing.T) {
	m := NewManager(DefaultBudget(200000))

	m.Register(&ContextBlock{
		ID: "sys-1", Type: BlockSystem,
		Content: "You are a helpful assistant", TokenCount: 10,
		Priority: 1.0, LastUsed: time.Now(),
	})
	m.Register(&ContextBlock{
		ID: "tool-1", Type: BlockTool,
		Content: "Tool: search — Search the web", TokenCount: 10,
		Priority: 0.5, LastUsed: time.Now(),
	})
	m.Register(&ContextBlock{
		ID: "mem-1", Type: BlockMemory,
		Content: "User prefers concise answers", TokenCount: 10,
		Priority: 0.5, LastUsed: time.Now(),
	})

	m.Load("sys-1")
	m.Load("tool-1")
	m.Load("mem-1")

	rendered := m.Render()

	// System should come before tools, tools before memory
	sysIdx := strings.Index(rendered, "## System")
	toolIdx := strings.Index(rendered, "## Available Tools")
	memIdx := strings.Index(rendered, "## Memory")

	if sysIdx < 0 || toolIdx < 0 || memIdx < 0 {
		t.Fatalf("missing sections in rendered output:\n%s", rendered)
	}
	if sysIdx > toolIdx {
		t.Error("system section should come before tools")
	}
	if toolIdx > memIdx {
		t.Error("tools section should come before memory")
	}
}

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		input    string
		expected int
	}{
		{"", 0},
		{"hello", 2},                      // 5 chars / 4 ≈ 2
		{"hello world this is a test", 7}, // 26 chars / 4 ≈ 7
	}

	for _, tt := range tests {
		got := EstimateTokens(tt.input)
		if got != tt.expected {
			t.Errorf("EstimateTokens(%q) = %d, want %d", tt.input, got, tt.expected)
		}
	}
}
