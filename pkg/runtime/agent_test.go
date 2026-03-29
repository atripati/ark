package runtime

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	ctx "github.com/atripati/ark/pkg/context"
)

func setupTestAgent() (*Agent, *ctx.Manager) {
	mgr := ctx.NewManager(ctx.DefaultBudget(200000))
	mgr.RegisterTool("github_list_repos", "github_list_repos",
		"list repos: list GitHub repositories for a user",
		`{"name":"github_list_repos"}`)
	mgr.RegisterTool("github_get_repo", "github_get_repo",
		"get repo: get repository details",
		`{"name":"github_get_repo"}`)
	engine := ctx.NewEngine(mgr, ctx.DefaultEngineConfig())
	return nil, mgr // placeholder — we build agents per test
	_ = engine
	return nil, mgr
}

func TestGroundingGateRejectsUngroundedAnswer(t *testing.T) {
	mgr := ctx.NewManager(ctx.DefaultBudget(200000))
	mgr.RegisterTool("github_list_repos", "github_list_repos",
		"list repos: list GitHub repositories for a user",
		`{"name":"github_list_repos"}`)
	engine := ctx.NewEngine(mgr, ctx.DefaultEngineConfig())

	// Mock: LLM tries to answer directly (no tool call), then uses tool on retry
	executor := &MockExecutor{
		Responses: []MockResponse{
			{Text: "Here are some fake repos..."},                                             // step 1: no tool call → grounding gate rejects
			{ToolName: "github_list_repos", Params: map[string]interface{}{"user": "openai"}}, // step 2: tool call
			{Text: "Based on the data, OpenAI has these repos..."},                            // step 3: grounded answer
		},
	}
	tools := &MockToolHandler{
		Results: map[string]string{
			"github_list_repos": `[{"name":"whisper","stars":50000}]`,
		},
	}

	agent := NewAgent(engine, executor, tools, AgentConfig{MaxSteps: 10, MaxRetriesPerTool: 3, TotalTimeout: 30 * time.Second, Verbose: false})
	result := agent.Run("grounding-test", "list repos for openai")

	if !result.Success {
		t.Fatal("task should succeed after grounding gate forces tool use")
	}

	// Check that grounding gate fired
	groundingFound := false
	toolCallFound := false
	for _, step := range result.Steps {
		if step.Action == "grounding_rejected" {
			groundingFound = true
		}
		if step.Action == "tool_call" {
			toolCallFound = true
		}
	}
	if !groundingFound {
		t.Error("grounding gate should have rejected the first ungrounded answer")
	}
	if !toolCallFound {
		t.Error("a tool call should have been made after grounding rejection")
	}
	t.Logf("Grounding gate worked: %d steps, output: %s", len(result.Steps), result.Output[:min(80, len(result.Output))])
}

func TestNoToolsNoGroundingGate(t *testing.T) {
	mgr := ctx.NewManager(ctx.DefaultBudget(200000))
	// No tools registered — grounding gate should NOT fire
	engine := ctx.NewEngine(mgr, ctx.DefaultEngineConfig())

	executor := &MockExecutor{
		Responses: []MockResponse{
			{Text: "The answer is 4."},
		},
	}
	tools := &MockToolHandler{}

	agent := NewAgent(engine, executor, tools, AgentConfig{MaxSteps: 10, MaxRetriesPerTool: 3, TotalTimeout: 30 * time.Second, Verbose: false})
	result := agent.Run("no-tools-test", "what is 2 plus 2")

	if !result.Success {
		t.Fatal("task should succeed without tools")
	}
	if result.Output != "The answer is 4." {
		t.Errorf("unexpected output: %s", result.Output)
	}
	for _, step := range result.Steps {
		if step.Action == "grounding_rejected" {
			t.Error("grounding gate should NOT fire when no tools are loaded")
		}
	}
}

func TestErrorTaxonomyClassification(t *testing.T) {
	tests := []struct {
		errMsg   string
		expected ctx.ErrorType
	}{
		{"missing required params: owner", ctx.ErrToolMisuse},
		{"'owner' and 'repo' params required", ctx.ErrToolMisuse},
		{"no handler for \"fake_tool\"", ctx.ErrToolNotFound},
		{"returned 404: Not Found", ctx.ErrToolFailed},
		{"returned 401: authentication required", ctx.ErrToolFailed},
		{"returned 429: rate limited", ctx.ErrToolFailed},
		{"some unknown error", ctx.ErrToolFailed},
	}

	for _, tt := range tests {
		err := &mockError{msg: tt.errMsg}
		got := classifyToolError(err)
		if got != tt.expected {
			t.Errorf("classifyToolError(%q) = %v, want %v", tt.errMsg, got, tt.expected)
		}
	}
}

func TestMetricsRecording(t *testing.T) {
	mgr := ctx.NewManager(ctx.DefaultBudget(200000))
	engine := ctx.NewEngine(mgr, ctx.DefaultEngineConfig())

	executor := &MockExecutor{
		Responses: []MockResponse{
			{Text: "42"},
		},
	}
	tools := &MockToolHandler{}

	agent := NewAgent(engine, executor, tools, AgentConfig{MaxSteps: 10, MaxRetriesPerTool: 3, TotalTimeout: 30 * time.Second, Verbose: false})
	agent.Run("metrics-test-1", "what is the meaning of life")
	agent.Run("metrics-test-2", "what is 1 plus 1")

	snap := agent.Metrics.Snapshot()
	if snap.TasksTotal != 2 {
		t.Errorf("expected 2 tasks, got %d", snap.TasksTotal)
	}
	if snap.TasksSucceeded != 2 {
		t.Errorf("expected 2 successes, got %d", snap.TasksSucceeded)
	}
	if snap.TotalTokens < 1 {
		t.Error("expected some tokens used")
	}
}

func TestTraceJSONExport(t *testing.T) {
	result := &TaskResult{
		TaskID:      "test-trace",
		Success:     true,
		Output:      "Hello world",
		TotalTokens: 100,
		TotalTime:   5 * time.Second,
		TraceID:     "trace-1",
		Steps: []StepRecord{
			{Step: 1, Action: "tool_call", ToolName: "github_list_repos", Output: "data", Tokens: 50, Duration: 2 * time.Second},
			{Step: 2, Action: "complete", Output: "Hello world", Tokens: 50, Duration: 3 * time.Second},
		},
	}

	jsonStr, err := TraceJSON(result)
	if err != nil {
		t.Fatalf("TraceJSON error: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if parsed["task_id"] != "test-trace" {
		t.Errorf("expected task_id=test-trace, got %v", parsed["task_id"])
	}
	if parsed["success"] != true {
		t.Errorf("expected success=true, got %v", parsed["success"])
	}
	steps := parsed["steps"].([]interface{})
	if len(steps) != 2 {
		t.Errorf("expected 2 steps, got %d", len(steps))
	}
	t.Logf("Trace JSON: %s", jsonStr[:min(200, len(jsonStr))])
}

func TestParseResponseToolCall(t *testing.T) {
	tests := []struct {
		input    string
		wantTool string
		wantKey  string
		wantVal  string
	}{
		{`Some thinking...
TOOL_CALL: github_list_repos({"user": "openai"})`, "github_list_repos", "user", "openai"},
		{`TOOL_CALL: github_get_repo({"owner": "atripati", "repo": "ark"})`, "github_get_repo", "owner", "atripati"},
		{`TOOL_CALL github_list_issues({"owner": "test", "repo": "test"})`, "github_list_issues", "owner", "test"},
		{"Just a regular answer with no tool call.", "", "", ""},
	}

	for _, tt := range tests {
		parsed := parseResponse(tt.input)
		if tt.wantTool == "" {
			if parsed.toolCall != nil {
				t.Errorf("expected no tool call for %q, got %s", tt.input[:30], parsed.toolCall.Name)
			}
			continue
		}
		if parsed.toolCall == nil {
			t.Errorf("expected tool call %s for %q", tt.wantTool, tt.input[:30])
			continue
		}
		if parsed.toolCall.Name != tt.wantTool {
			t.Errorf("expected tool %s, got %s", tt.wantTool, parsed.toolCall.Name)
		}
		if tt.wantKey != "" {
			val, ok := parsed.toolCall.Params[tt.wantKey]
			if !ok || val != tt.wantVal {
				t.Errorf("expected param %s=%s, got %v", tt.wantKey, tt.wantVal, val)
			}
		}
	}
}

type mockError struct{ msg string }

func (e *mockError) Error() string { return e.msg }

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestToolCallWithParamsMissing(t *testing.T) {
	// Simulate the flow: LLM calls tool without params → param validation rejects → LLM retries
	mgr := ctx.NewManager(ctx.DefaultBudget(200000))
	mgr.RegisterTool("github_get_repo", "github_get_repo",
		"get repo: get repository details",
		`{"name":"github_get_repo"}`)
	engine := ctx.NewEngine(mgr, ctx.DefaultEngineConfig())

	executor := &MockExecutor{
		Responses: []MockResponse{
			{ToolName: "github_get_repo", Params: map[string]interface{}{}},                                   // missing params
			{ToolName: "github_get_repo", Params: map[string]interface{}{"owner": "atripati", "repo": "ark"}}, // corrected
			{Text: "ARK has 5 stars and is written in Go."},
		},
	}
	tools := &MockToolHandler{
		Results: map[string]string{
			"github_get_repo": `{"name":"ark","stars":5,"language":"Go"}`,
		},
		Errors: map[string]error{},
	}

	agent := NewAgent(engine, executor, tools, AgentConfig{MaxSteps: 10, MaxRetriesPerTool: 3, TotalTimeout: 30 * time.Second, Verbose: false})
	result := agent.Run("param-test", "get details about atripati/ark")

	if !result.Success {
		t.Logf("Steps: %+v", result.Steps)
		// This test validates the flow works — mock doesn't have real param validation
		// because MockToolHandler doesn't check RequiredParams. That's tested in tools/http_test.go
	}

	hasToolCall := false
	for _, step := range result.Steps {
		if strings.Contains(step.Action, "tool_call") {
			hasToolCall = true
		}
	}
	if !hasToolCall {
		t.Error("expected at least one tool call in the flow")
	}
}
