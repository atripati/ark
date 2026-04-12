package runtime

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	ctx "github.com/atripati/ark/pkg/context"
	"github.com/atripati/ark/pkg/cost"
)

type Executor interface {
	Execute(context string, task string) (*ModelResponse, error)
}

type ModelResponse struct {
	Text         string
	ToolCall     *ToolCall
	TokensUsed   int
	InputTokens  int
	OutputTokens int
	Latency      time.Duration
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

type StepAwareExecutor interface {
	Executor
	SetStep(step int, stepType string)
}

func classifyAgentStep(step int, isRetry bool, prevAction string) string {
	if isRetry {
		return "retry"
	}
	if prevAction == "tool_call" {
		if step >= 3 {
			return "complete"
		}
	}
	return "tool_call"
}

type Agent struct {
	engine   *ctx.Engine
	executor Executor
	tools    ToolHandler
	config   AgentConfig
	Metrics  *RuntimeMetrics
}

type AgentConfig struct {
	MaxSteps          int
	MaxRetriesPerTool int
	TotalTimeout      time.Duration
	Verbose           bool

	Provider       string
	Model          string
	MaxCostPerTask float64
}

func DefaultAgentConfig() AgentConfig {
	return AgentConfig{
		MaxSteps:          5,
		MaxRetriesPerTool: 2,
		TotalTimeout:      60 * time.Second,
		Verbose:           true,
	}
}

func NewAgent(engine *ctx.Engine, executor Executor, tools ToolHandler, config AgentConfig) *Agent {
	return &Agent{
		engine:   engine,
		executor: executor,
		tools:    tools,
		config:   config,
		Metrics:  NewRuntimeMetrics(),
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
	CostReport  *cost.CostReport
}

type StepRecord struct {
	Step         int
	Action       string
	ToolName     string
	Input        string
	Output       string
	InputTokens  int
	OutputTokens int
	Duration     time.Duration
	Strategy     string
}

type RuntimeMetrics struct {
	mu               sync.Mutex
	TasksTotal       int                        `json:"tasks_total"`
	TasksSucceeded   int                        `json:"tasks_succeeded"`
	TasksFailed      int                        `json:"tasks_failed"`
	ToolCallsTotal   int                        `json:"tool_calls_total"`
	ToolCallsSuccess int                        `json:"tool_calls_success"`
	ToolCallsFailed  int                        `json:"tool_calls_failed"`
	GroundingRejects int                        `json:"grounding_rejects"`
	ParamRejects     int                        `json:"param_rejects"`
	TotalTokens      int                        `json:"total_tokens"`
	TotalLatency     time.Duration              `json:"total_latency_ms"`
	AvgLatency       time.Duration              `json:"avg_latency_ms"`
	ToolLatencies    map[string][]time.Duration `json:"-"`
}

func NewRuntimeMetrics() *RuntimeMetrics {
	return &RuntimeMetrics{ToolLatencies: make(map[string][]time.Duration)}
}

func (m *RuntimeMetrics) recordTask(result *TaskResult) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.TasksTotal++
	if result.Success {
		m.TasksSucceeded++
	} else {
		m.TasksFailed++
	}
	m.TotalTokens += result.TotalTokens
	m.TotalLatency += result.TotalTime
	if m.TasksTotal > 0 {
		m.AvgLatency = m.TotalLatency / time.Duration(m.TasksTotal)
	}
	for _, step := range result.Steps {
		switch step.Action {
		case "tool_call":
			m.ToolCallsTotal++
			m.ToolCallsSuccess++
			if step.ToolName != "" {
				m.ToolLatencies[step.ToolName] = append(m.ToolLatencies[step.ToolName], step.Duration)
			}
		case "tool_call_retry":
			m.ToolCallsTotal++
			m.ToolCallsFailed++
		case "grounding_rejected":
			m.GroundingRejects++
		}
	}
}

func (m *RuntimeMetrics) Snapshot() RuntimeMetrics {
	m.mu.Lock()
	defer m.mu.Unlock()
	return RuntimeMetrics{
		TasksTotal: m.TasksTotal, TasksSucceeded: m.TasksSucceeded,
		TasksFailed: m.TasksFailed, ToolCallsTotal: m.ToolCallsTotal,
		ToolCallsSuccess: m.ToolCallsSuccess, ToolCallsFailed: m.ToolCallsFailed,
		GroundingRejects: m.GroundingRejects, ParamRejects: m.ParamRejects,
		TotalTokens: m.TotalTokens, TotalLatency: m.TotalLatency, AvgLatency: m.AvgLatency,
	}
}

func (m *RuntimeMetrics) ToolP50(toolName string) time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	samples := m.ToolLatencies[toolName]
	if len(samples) == 0 {
		return 0
	}
	sorted := make([]time.Duration, len(samples))
	copy(sorted, samples)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j] < sorted[j-1]; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	return sorted[len(sorted)/2]
}

func TraceJSON(result *TaskResult) (string, error) {
	type jsonStep struct {
		Step         int    `json:"step"`
		Action       string `json:"action"`
		Tool         string `json:"tool,omitempty"`
		Output       string `json:"output"`
		InputTokens  int    `json:"input_tokens"`
		OutputTokens int    `json:"output_tokens"`
		Duration     string `json:"duration"`
	}
	type jsonTrace struct {
		TaskID   string     `json:"task_id"`
		Success  bool       `json:"success"`
		Output   string     `json:"output"`
		Steps    []jsonStep `json:"steps"`
		Tokens   int        `json:"total_tokens"`
		Duration string     `json:"total_duration"`
		TraceID  string     `json:"trace_id"`
	}
	trace := jsonTrace{
		TaskID: result.TaskID, Success: result.Success, Output: result.Output,
		Tokens: result.TotalTokens, Duration: result.TotalTime.Round(time.Millisecond).String(),
		TraceID: result.TraceID,
	}
	for _, s := range result.Steps {
		trace.Steps = append(trace.Steps, jsonStep{
			Step: s.Step, Action: s.Action, Tool: s.ToolName,
			Output: truncateStr(s.Output, 200), InputTokens: s.InputTokens,
			OutputTokens: s.OutputTokens,
			Duration:     s.Duration.Round(time.Millisecond).String(),
		})
	}
	data, err := json.MarshalIndent(trace, "", "  ")
	return string(data), err
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func (a *Agent) Run(taskID, task string) *TaskResult {
	startTime := time.Now()
	result := &TaskResult{
		TaskID: taskID,
		Steps:  make([]StepRecord, 0),
	}

	pricing := cost.ModelPricing(a.config.Provider, a.config.Model)
	costTracker := cost.NewTracker(pricing)
	costTracker.SetTaskID(taskID)
	if a.config.MaxCostPerTask > 0 {
		costTracker.SetBudget(a.config.MaxCostPerTask)
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

	toolContext := a.engine.Manager().Render()
	systemPrompt := buildSystemPrompt(toolContext, plan.ToolsLoaded)

	var history []message
	history = append(history, message{Role: "user", Content: task})

	toolsWereLoaded := len(plan.ToolsLoaded) > 0
	toolCallSucceeded := false
	toolRetries := make(map[string]int)
	deadline := time.Now().Add(a.config.TotalTimeout)

	for step := 0; step < a.config.MaxSteps; step++ {
		if time.Now().After(deadline) {
			if a.config.Verbose {
				fmt.Printf("├─ Step %d: TIMEOUT — total execution exceeded %v\n", step+1, a.config.TotalTimeout)
			}
			result.Steps = append(result.Steps, StepRecord{
				Step: step + 1, Action: "timeout",
				Output: fmt.Sprintf("execution_limit_exceeded: total timeout %v", a.config.TotalTimeout),
			})
			break
		}

		if costTracker.BudgetExceeded() {
			if a.config.Verbose {
				fmt.Printf("├─ Step %d: BUDGET EXCEEDED — cost $%.6f exceeds limit $%.4f\n",
					step+1, costTracker.RunningCost(), a.config.MaxCostPerTask)
			}
			result.Steps = append(result.Steps, StepRecord{
				Step: step + 1, Action: "budget_exceeded",
				Output: fmt.Sprintf("cost_limit_exceeded: $%.6f >= $%.4f", costTracker.RunningCost(), a.config.MaxCostPerTask),
			})
			break
		}

		stepStart := time.Now()

		prompt := buildPrompt(history)

		// these tell the router what step type this is (if router is being used).
		// Finally this enables per-step model routing: tool calls → fast model, reasoning → strong model.
		if sa, ok := a.executor.(StepAwareExecutor); ok {
			isRetry := step > 0 && len(result.Steps) > 0 && strings.HasSuffix(result.Steps[len(result.Steps)-1].Action, "retry")
			prevAction := ""
			if len(result.Steps) > 0 {
				prevAction = result.Steps[len(result.Steps)-1].Action
			}
			stepType := classifyAgentStep(step+1, isRetry, prevAction)
			sa.SetStep(step+1, stepType)
		}

		response, err := a.executor.Execute(systemPrompt, prompt)
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
				toolContext = a.engine.Manager().Render()
				systemPrompt = buildSystemPrompt(toolContext, plan.ToolsLoaded)
				if a.config.Verbose {
					fmt.Printf("├─ Adapted: %s → loaded %d tools (%d tokens)\n",
						newPlan.Strategy, len(newPlan.ToolsLoaded), newPlan.TokensUsed)
				}
				continue
			}
			break
		}

		result.TotalTokens += response.TokensUsed

		var toolCall *ToolCall
		var finalText string

		if response.ToolCall != nil {
			toolCall = response.ToolCall
		} else {
			parsed := parseResponse(response.Text)
			if parsed.toolCall != nil {
				toolCall = parsed.toolCall
			} else {
				finalText = parsed.text
			}
		}

		if toolCall != nil {
			if toolRetries[toolCall.Name] >= a.config.MaxRetriesPerTool {
				if a.config.Verbose {
					fmt.Printf("├─ Step %d: RETRY_EXHAUSTED — %s exceeded %d retries, forcing different tool\n",
						step+1, toolCall.Name, a.config.MaxRetriesPerTool)
				}
				result.Steps = append(result.Steps, StepRecord{
					Step: step + 1, Action: "retry_exhausted", ToolName: toolCall.Name,
					Output: fmt.Sprintf("tool %s exceeded retry budget (%d)", toolCall.Name, a.config.MaxRetriesPerTool),
				})
				history = append(history, message{
					Role:    "user",
					Content: fmt.Sprintf("Tool %s has failed too many times. Use a DIFFERENT tool or explain why no tool can answer this.", toolCall.Name),
				})
				continue
			}

			if a.config.Verbose {
				fmt.Printf("├─ Step %d: TOOL_CALL — %s\n", step+1, toolCall.Name)
			}

			toolErr := a.tools.Handle(toolCall)

			record := StepRecord{
				Step:        step + 1,
				Action:      "tool_call",
				ToolName:    toolCall.Name,
				Input:       fmt.Sprintf("%v", toolCall.Params),
				InputTokens: response.InputTokens, OutputTokens: response.OutputTokens,
				Duration: time.Since(stepStart),
				Strategy: plan.Strategy,
			}

			if toolErr != nil {
				record.Output = toolErr.Error()
				record.Action = "tool_call_retry"
				toolRetries[toolCall.Name]++

				if a.config.Verbose {
					fmt.Printf("│  ↳ Failed: %v\n", toolErr)
				}

				execResult := ctx.ExecutionResult{
					Success:     false,
					ToolUsed:    toolCall.Name,
					ToolsFailed: []string{toolCall.Name},
					ErrorType:   classifyToolError(toolErr),
					ErrorMsg:    toolErr.Error(),
					TokensUsed:  response.TokensUsed,
					Latency:     response.Latency,
				}
				newPlan := a.engine.AdaptContext(plan, execResult)
				if newPlan != nil {
					plan = newPlan
					toolContext = a.engine.Manager().Render()
					systemPrompt = buildSystemPrompt(toolContext, plan.ToolsLoaded)
					if a.config.Verbose {
						fmt.Printf("├─ Adapted: %s → %d tools\n",
							newPlan.Strategy, len(newPlan.ToolsLoaded))
					}
				}

				history = append(history, message{
					Role:    "assistant",
					Content: fmt.Sprintf("I tried to call %s but it failed: %s", toolCall.Name, toolErr.Error()),
				})
			} else {
				record.Output = toolCall.Result
				toolCallSucceeded = true

				if a.config.Verbose {
					preview := toolCall.Result
					if len(preview) > 80 {
						preview = preview[:80] + "..."
					}
					fmt.Printf("│  ↳ Result: %s\n", preview)
				}

				execResult := ctx.ExecutionResult{
					Success:    true,
					ToolUsed:   toolCall.Name,
					TokensUsed: response.TokensUsed,
					Latency:    response.Latency,
				}
				a.engine.AdaptContext(plan, execResult)

				history = append(history, message{
					Role:    "assistant",
					Content: fmt.Sprintf("I called %s and got results.", toolCall.Name),
				})
				history = append(history, message{
					Role: "user",
					Content: fmt.Sprintf("Tool %s returned this data:\n%s\n\nUsing ONLY the data above, answer the original question. Do NOT make up information.",
						toolCall.Name, toolCall.Result),
				})
			}

			result.Steps = append(result.Steps, record)
			continue
		}

		if toolsWereLoaded && !toolCallSucceeded {
			if a.config.Verbose {
				fmt.Printf("├─ Step %d: GROUNDING GATE — rejecting ungrounded answer, forcing tool use\n", step+1)
			}
			result.Steps = append(result.Steps, StepRecord{
				Step: step + 1, Action: "grounding_rejected",
				Output:      "Answer rejected: tools available but none called successfully",
				InputTokens: response.InputTokens, OutputTokens: response.OutputTokens, Duration: time.Since(stepStart),
			})
			history = append(history, message{
				Role:    "user",
				Content: "You MUST call a tool before answering. Do NOT answer from memory. Use one of the available tools to get real data. Respond with TOOL_CALL: tool_name({\"params\"}).",
			})
			continue
		}

		record := StepRecord{
			Step:        step + 1,
			Action:      "complete",
			Output:      finalText,
			InputTokens: response.InputTokens, OutputTokens: response.OutputTokens,
			Duration: response.Latency,
			Strategy: plan.Strategy,
		}
		result.Steps = append(result.Steps, record)
		result.Success = true
		result.Output = finalText

		if a.config.Verbose {
			preview := finalText
			if len(preview) > 80 {
				preview = preview[:80] + "..."
			}
			fmt.Printf("├─ Step %d: COMPLETE — %s\n", step+1, preview)
		}
		break
	}

	result.TotalTime = time.Since(startTime)

	for _, step := range result.Steps {
		costTracker.RecordStep(step.Step, step.InputTokens, step.OutputTokens, step.Action, step.ToolName, step.Duration)
	}

	result.CostReport = costTracker.Report()

	for tool, toolCost := range result.CostReport.CostByTool {
		a.engine.RecordToolCost(tool, toolCost)
	}

	if a.config.Verbose {
		fmt.Printf("│\n")
		if result.CostReport.TotalCost > 0 {
			fmt.Printf("└─ Done: %d steps, %d tokens, %v | Cost: $%.6f\n",
				len(result.Steps), result.TotalTokens, result.TotalTime.Round(time.Millisecond),
				result.CostReport.TotalCost)
		} else {
			fmt.Printf("└─ Done: %d steps, %d tokens, %v | Cost: $0 (local model)\n",
				len(result.Steps), result.TotalTokens, result.TotalTime.Round(time.Millisecond))
		}
	}

	a.Metrics.recordTask(result)

	return result
}

func classifyToolError(err error) ctx.ErrorType {
	msg := err.Error()
	if strings.Contains(msg, "missing required params") || strings.Contains(msg, "params required") {
		return ctx.ErrToolMisuse
	}
	if strings.Contains(msg, "no handler") {
		return ctx.ErrToolNotFound
	}
	if strings.Contains(msg, "404") || strings.Contains(msg, "Not Found") {
		return ctx.ErrToolFailed
	}
	if strings.Contains(msg, "401") || strings.Contains(msg, "403") || strings.Contains(msg, "auth") {
		return ctx.ErrToolFailed
	}
	if strings.Contains(msg, "429") || strings.Contains(msg, "rate") {
		return ctx.ErrToolFailed
	}
	return ctx.ErrToolFailed
}

func buildSystemPrompt(toolContext string, loadedTools []string) string {
	if len(loadedTools) == 0 {
		return "You are a helpful assistant. Answer the user's question directly and concisely."
	}

	var sb strings.Builder
	sb.WriteString("You are an AI agent with access to real tools. You MUST use tools to answer questions about real-world data (GitHub repos, issues, users, etc.). Do NOT guess or make up information — always call the appropriate tool first.\n\n")

	sb.WriteString(toolContext)
	sb.WriteString("\n\n")

	sb.WriteString("TO CALL A TOOL, respond with EXACTLY this format on a line by itself:\n")
	sb.WriteString("TOOL_CALL: tool_name({\"param1\": \"value1\", \"param2\": \"value2\"})\n\n")

	sb.WriteString("EXAMPLES:\n")
	sb.WriteString("TOOL_CALL: github_list_repos({\"user\": \"atripati\"})\n")
	sb.WriteString("TOOL_CALL: github_get_repo({\"owner\": \"atripati\", \"repo\": \"ark\"})\n")
	sb.WriteString("TOOL_CALL: github_list_issues({\"owner\": \"atripati\", \"repo\": \"ark\"})\n\n")

	sb.WriteString("RULES:\n")
	sb.WriteString("1. If asked about GitHub repos, issues, PRs, or users → ALWAYS call a github tool first.\n")
	sb.WriteString("2. Use ONLY the exact tool names shown above.\n")
	sb.WriteString("3. ONE tool call per response.\n")
	sb.WriteString("4. After receiving tool results, summarize them for the user.\n")
	sb.WriteString("5. NEVER fabricate repository names, issue numbers, or user data.\n")

	return sb.String()
}

type message struct {
	Role    string
	Content string
}

func buildPrompt(history []message) string {
	if len(history) == 1 {
		return history[0].Content
	}

	var sb strings.Builder
	for _, msg := range history {
		switch msg.Role {
		case "user":
			sb.WriteString("User: ")
		case "assistant":
			sb.WriteString("Assistant: ")
		}
		sb.WriteString(msg.Content)
		sb.WriteString("\n\n")
	}
	sb.WriteString("Assistant: ")
	return sb.String()
}

type parsedResponse struct {
	text     string
	toolCall *ToolCall
}

func parseResponse(text string) parsedResponse {
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "TOOL_CALL:") {
			rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "TOOL_CALL:"))
			return parseToolCallLine(rest)
		}
		if strings.HasPrefix(trimmed, "TOOL_CALL ") {
			rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "TOOL_CALL "))
			return parseToolCallLine(rest)
		}
	}

	return parsedResponse{text: text}
}

func parseToolCallLine(s string) parsedResponse {
	parenIdx := strings.Index(s, "(")
	if parenIdx < 0 {
		toolName := strings.TrimSpace(s)
		if toolName != "" {
			return parsedResponse{
				toolCall: &ToolCall{
					Name:   toolName,
					Params: make(map[string]interface{}),
				},
			}
		}
		return parsedResponse{text: s}
	}

	toolName := strings.TrimSpace(s[:parenIdx])

	closeIdx := strings.LastIndex(s, ")")
	if closeIdx <= parenIdx {
		closeIdx = len(s)
	}
	paramsStr := s[parenIdx+1 : closeIdx]

	params := parseParams(paramsStr)

	return parsedResponse{
		toolCall: &ToolCall{
			Name:   toolName,
			Params: params,
		},
	}
}

func parseParams(s string) map[string]interface{} {
	s = strings.TrimSpace(s)
	params := make(map[string]interface{})

	if s == "" {
		return params
	}

	if strings.HasPrefix(s, "{") {
		if err := json.Unmarshal([]byte(s), &params); err == nil {
			return params
		}
	}

	pairs := strings.Split(s, ",")
	for _, pair := range pairs {
		pair = strings.TrimSpace(pair)
		if eqIdx := strings.Index(pair, "="); eqIdx > 0 {
			key := strings.TrimSpace(pair[:eqIdx])
			val := strings.TrimSpace(pair[eqIdx+1:])
			val = strings.Trim(val, "\"'")
			params[key] = val
		} else if pair != "" {
			if _, exists := params["query"]; !exists {
				params["query"] = pair
			}
		}
	}

	return params
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
			Text:         "[Mock: no more responses queued]",
			TokensUsed:   10,
			InputTokens:  6,
			OutputTokens: 4,
			Latency:      5 * time.Millisecond,
		}, nil
	}

	resp := m.Responses[m.callIndex]
	m.callIndex++

	if resp.Error != nil {
		return nil, resp.Error
	}

	mr := &ModelResponse{
		Text:         resp.Text,
		TokensUsed:   50,
		InputTokens:  20,
		OutputTokens: 30,
		Latency:      20 * time.Millisecond,
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

func RunDemo(mgr *ctx.Manager) {
	fmt.Println()
	fmt.Println(strings.Repeat("═", 60))
	fmt.Println("  ARK Agent Runtime Demo")
	fmt.Println(strings.Repeat("═", 60))

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
