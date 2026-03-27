package runtime

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	ctx "github.com/atripati/ark/pkg/context"
)

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
	Action   string
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

	toolContext := a.engine.Manager().Render()
	systemPrompt := buildSystemPrompt(toolContext, plan.ToolsLoaded)

	var history []message
	history = append(history, message{Role: "user", Content: task})

	for step := 0; step < a.config.MaxSteps; step++ {
		stepStart := time.Now()

		prompt := buildPrompt(history)

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
				record.Action = "tool_call_retry"

				if a.config.Verbose {
					fmt.Printf("│  ↳ Failed: %v\n", toolErr)
				}

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
		record := StepRecord{
			Step:     step + 1,
			Action:   "complete",
			Output:   finalText,
			Tokens:   response.TokensUsed,
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

	if a.config.Verbose {
		fmt.Printf("│\n")
		fmt.Printf("└─ Done: %d steps, %d tokens, %v\n",
			len(result.Steps), result.TotalTokens, result.TotalTime.Round(time.Millisecond))
	}

	return result
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

	// Try JSON first
	if strings.HasPrefix(s, "{") {
		if err := json.Unmarshal([]byte(s), &params); err == nil {
			return params
		}
	}

	// Fallback: key=value pairs
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
