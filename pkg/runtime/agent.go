package runtime

import (
	"fmt"
	"strings"
	"time"

	ctx "github.com/atripati/ark/pkg/context"
)

// Implement this for your model provider (Anthropic, OpenAI, Ollama, etc.)
type Executor interface {
	Execute(context string, task string) (*ModelResponse, error)
}

type ModelResponse struct {
	Text       string
	ToolCall   *ToolCall
	TokensUsed int
	Latency    time.Duration
}

type ToolCall struct {
	Name   string
	Params map[string]interface{}
	Result string
	Error  error
}

type ToolHandler interface {
	Handle(call *ToolCall) error
}

type Agent struct {
	engine   *ctx.Engine
	executor Executor
	tools    ToolHandler
	config   AgentConfig
}

type AgentConfig struct {
	MaxSteps int
	Verbose  bool
}

func DefaultAgentConfig() AgentConfig {
	return AgentConfig{
		MaxSteps: 10,
		Verbose:  true,
	}
}

func NewAgent(engine *ctx.Engine, executor Executor, tools ToolHandler, config AgentConfig) *Agent {
	return &Agent{
		engine:   engine,
		executor: executor,
		tools:    tools,
		config:   config,
	}
}

type TaskResult struct {
	TaskID      string
	Success     bool
	Output      string
	Steps       []StepRecord
	TotalTokens int
	TotalTime   time.Duration
	TraceID     string
}
type StepRecord struct {
	Step     int
	Action   string // "think", "tool_call", "adapt", "complete"
	ToolName string
	Input    string
	Output   string
	Tokens   int
	Duration time.Duration
	Strategy string
}

func (a *Agent) Run(taskID, task string) *TaskResult {
	startTime := time.Now()
	result := &TaskResult{
		TaskID: taskID,
		Steps:  make([]StepRecord, 0),
	}

	if a.config.Verbose {
		fmt.Printf("\n┌─ ARK Agent: Task %q\n", taskID)
		fmt.Printf("│  %s\n", task)
		fmt.Printf("│\n")
	}

	plan := a.engine.PrepareContext(taskID, task)
	result.TraceID = plan.TraceID

	if a.config.Verbose {
		fmt.Printf("├─ Context: loaded %d tools (%d tokens) [strategy: %s]\n",
			len(plan.ToolsLoaded), plan.TokensUsed, plan.Strategy)
	}

	for step := 0; step < a.config.MaxSteps; step++ {
		stepStart := time.Now()

		contextStr := a.engine.Manager().Render()

		response, err := a.executor.Execute(contextStr, task)
		if err != nil {
			record := StepRecord{
				Step:     step + 1,
				Action:   "error",
				Output:   err.Error(),
				Duration: time.Since(stepStart),
			}
			result.Steps = append(result.Steps, record)

			if a.config.Verbose {
				fmt.Printf("├─ Step %d: ERROR — %v\n", step+1, err)
			}
			execResult := ctx.ExecutionResult{
				Success:   false,
				ErrorType: ctx.ErrToolFailed,
				ErrorMsg:  err.Error(),
			}
			newPlan := a.engine.AdaptContext(plan, execResult)
			if newPlan != nil {
				plan = newPlan
				if a.config.Verbose {
					fmt.Printf("├─ Adapted: %s → loaded %d tools (%d tokens)\n",
						newPlan.Strategy, len(newPlan.ToolsLoaded), newPlan.TokensUsed)
				}
				continue
			}
			break
		}

		result.TotalTokens += response.TokensUsed
		if response.ToolCall == nil {
			record := StepRecord{
				Step:     step + 1,
				Action:   "complete",
				Output:   response.Text,
				Tokens:   response.TokensUsed,
				Duration: response.Latency,
				Strategy: plan.Strategy,
			}
			result.Steps = append(result.Steps, record)
			result.Success = true
			result.Output = response.Text

			if a.config.Verbose {
				outputPreview := response.Text
				if len(outputPreview) > 80 {
					outputPreview = outputPreview[:80] + "..."
				}
				fmt.Printf("├─ Step %d: COMPLETE — %s\n", step+1, outputPreview)
			}
			break
		}

		toolCall := response.ToolCall

		if a.config.Verbose {
			fmt.Printf("├─ Step %d: TOOL_CALL — %s\n", step+1, toolCall.Name)
		}

		toolErr := a.tools.Handle(toolCall)

		record := StepRecord{
			Step:     step + 1,
			Action:   "tool_call",
			ToolName: toolCall.Name,
			Input:    fmt.Sprintf("%v", toolCall.Params),
			Tokens:   response.TokensUsed,
			Duration: time.Since(stepStart),
			Strategy: plan.Strategy,
		}

		if toolErr != nil {
			record.Output = toolErr.Error()
			execResult := ctx.ExecutionResult{
				Success:     false,
				ToolUsed:    toolCall.Name,
				ToolsFailed: []string{toolCall.Name},
				ErrorType:   ctx.ErrToolFailed,
				ErrorMsg:    toolErr.Error(),
				TokensUsed:  response.TokensUsed,
				Latency:     response.Latency,
			}

			newPlan := a.engine.AdaptContext(plan, execResult)
			if newPlan != nil {
				plan = newPlan
				record.Action = "tool_call_retry"
				if a.config.Verbose {
					fmt.Printf("│  ↳ Failed, adapting: %s → %d tools\n",
						newPlan.Strategy, len(newPlan.ToolsLoaded))
				}
			}
		} else {
			record.Output = toolCall.Result
			execResult := ctx.ExecutionResult{
				Success:    true,
				ToolUsed:   toolCall.Name,
				TokensUsed: response.TokensUsed,
				Latency:    response.Latency,
			}
			a.engine.AdaptContext(plan, execResult)

			if a.config.Verbose {
				outputPreview := toolCall.Result
				if len(outputPreview) > 60 {
					outputPreview = outputPreview[:60] + "..."
				}
				fmt.Printf("│  ↳ Result: %s\n", outputPreview)
			}
		}

		result.Steps = append(result.Steps, record)
	}

	result.TotalTime = time.Since(startTime)

	if a.config.Verbose {
		fmt.Printf("│\n")
		fmt.Printf("└─ Done: %d steps, %d tokens, %v\n",
			len(result.Steps), result.TotalTokens, result.TotalTime.Round(time.Millisecond))
	}

	return result
}

type MockExecutor struct {
	Responses []MockResponse
	callIndex int
}
type MockResponse struct {
	Text     string
	ToolName string
	Params   map[string]interface{}
	Error    error
}

func (m *MockExecutor) Execute(context string, task string) (*ModelResponse, error) {
	if m.callIndex >= len(m.Responses) {
		return &ModelResponse{
			Text:       "[Mock: no more responses queued]",
			TokensUsed: 10,
			Latency:    5 * time.Millisecond,
		}, nil
	}

	resp := m.Responses[m.callIndex]
	m.callIndex++

	if resp.Error != nil {
		return nil, resp.Error
	}

	mr := &ModelResponse{
		Text:       resp.Text,
		TokensUsed: 50,
		Latency:    20 * time.Millisecond,
	}

	if resp.ToolName != "" {
		mr.ToolCall = &ToolCall{
			Name:   resp.ToolName,
			Params: resp.Params,
		}
	}

	return mr, nil
}

type MockToolHandler struct {
	Results map[string]string
	Errors  map[string]error
}

func (m *MockToolHandler) Handle(call *ToolCall) error {
	if err, ok := m.Errors[call.Name]; ok {
		call.Error = err
		return err
	}
	if result, ok := m.Results[call.Name]; ok {
		call.Result = result
		return nil
	}
	call.Result = fmt.Sprintf("[Mock result for %s]", call.Name)
	return nil
}

// it will show two scenarios:
//  1. Happy path: load tools → call → succeed
//  2. Failure recovery: load tools → fail → adapt context → retry → succeed
func RunDemo(mgr *ctx.Manager) {
	fmt.Println()
	fmt.Println(strings.Repeat("═", 60))
	fmt.Println("  ARK Agent Runtime Demo")
	fmt.Println(strings.Repeat("═", 60))

	// ── Scenario 1: Happy path ──
	fmt.Println()
	fmt.Println("  ── Scenario 1: Happy Path ──")
	fmt.Println("  Task: Create a pull request on github")
	fmt.Println()

	engine1 := ctx.NewEngine(mgr, ctx.DefaultEngineConfig())

	executor1 := &MockExecutor{
		Responses: []MockResponse{
			{ToolName: "github_create_pr", Params: map[string]interface{}{
				"repo":  "atripati/ark",
				"title": "feat: add dynamic context engine",
			}},
			{Text: "Done! PR #42 is open at github.com/atripati/ark/pull/42"},
		},
	}

	tools1 := &MockToolHandler{
		Results: map[string]string{
			"github_create_pr": `{"id": 42, "url": "https://github.com/atripati/ark/pull/42", "state": "open"}`,
		},
	}

	agent1 := NewAgent(engine1, executor1, tools1, DefaultAgentConfig())
	result1 := agent1.Run("happy-path", "create a pull request on github for the new feature")

	fmt.Println()
	fmt.Println("  Trace:")
	fmt.Println(engine1.TracerRef().PrintTrace(result1.TraceID))

	// ── Scenario 2: Failure → Adapt → Retry ──
	fmt.Println("  ── Scenario 2: Failure → Adapt → Retry ──")
	fmt.Println("  Task: Search jira issues (tool fails first, engine adapts)")
	fmt.Println()

	engine2 := ctx.NewEngine(mgr, ctx.DefaultEngineConfig())

	executor2 := &MockExecutor{
		Responses: []MockResponse{
			{ToolName: "jira_search_issues", Params: map[string]interface{}{
				"query": "assigned to me",
			}},
			{ToolName: "jira_list_issues", Params: map[string]interface{}{
				"filter": "assignee=me",
			}},
			{Text: "Found 3 open issues assigned to you: ARK-101, ARK-102, ARK-103"},
		},
	}

	tools2 := &MockToolHandler{
		Results: map[string]string{
			"jira_list_issues": `[{"key":"ARK-101","summary":"Fix token counting"},{"key":"ARK-102","summary":"Add MCP connector"},{"key":"ARK-103","summary":"Write docs"}]`,
		},
		Errors: map[string]error{
			"jira_search_issues": fmt.Errorf("Jira API returned 503: service temporarily unavailable"),
		},
	}

	agent2 := NewAgent(engine2, executor2, tools2, DefaultAgentConfig())
	result2 := agent2.Run("retry-demo", "search jira issues assigned to me")

	fmt.Println()
	fmt.Println("  Trace:")
	fmt.Println(engine2.TracerRef().PrintTrace(result2.TraceID))

	// Summary
	fmt.Println(strings.Repeat("─", 60))
	fmt.Printf("  Scenario 1: success=%v, steps=%d, strategy=minimal\n",
		result1.Success, len(result1.Steps))
	fmt.Printf("  Scenario 2: success=%v, steps=%d, strategy=adapt→retry\n",
		result2.Success, len(result2.Steps))
	fmt.Println()
	fmt.Println("  This is the difference between a static optimizer and")
	fmt.Println("  a context decision engine. ARK observes, adapts, recovers.")
	fmt.Println()
}
