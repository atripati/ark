// Package cost provides decision-level cost attribution for AI agent runtimes.
//
// Unlike request-level billing, this package tracks cost at the decision level:
// each reasoning step, tool call, and adaptation has an individual cost.
// This enables:
//
//   - Per-feature cost attribution (which features are worth keeping?)
//   - Budget-constrained execution (stop before exceeding $X)
//   - Cost-aware tool ranking (prefer cheaper tools when accuracy is equal)
//   - Decision cost graphs (where is the money going?)
//
// The cost model is provider-aware: it uses published token pricing from
// Anthropic, OpenAI, and others. For local models (Ollama), cost is $0.
//
// Usage:
//
//	tracker := cost.NewTracker(cost.ModelPricing("anthropic", "claude-sonnet-4-20250514"))
//	tracker.SetBudget(0.01) // $0.01 max per task
//	tracker.SetAttribution("user-123", "repo-analysis", "session-abc")
//
//	// During execution:
//	tracker.RecordStep(step, inputTokens, outputTokens, toolName, latency)
//
//	// After execution:
//	report := tracker.Report()
//	fmt.Println(report.TotalCost)      // $0.0034
//	fmt.Println(report.CostGraph)      // per-step breakdown
//	fmt.Println(report.BudgetUsedPct)  // 34%
package cost

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ──────────────────────────────────────────────────────────
// Pricing — provider/model → cost per token
// ──────────────────────────────────────────────────────────

// TokenPrice defines the cost per token for a model.
// Prices are in USD per single token (not per million).
type TokenPrice struct {
	Provider       string  // anthropic, openai, ollama, custom
	Model          string  // claude-sonnet-4-20250514, gpt-4o, llama3, etc.
	InputPerToken  float64 // USD per input token
	OutputPerToken float64 // USD per output token
	DisplayName    string  // human-readable name for reports
}

// priceTable maps "provider/model" to pricing.
// Updated from published pricing as of March 2026.
var priceTable = map[string]TokenPrice{
	// Anthropic
	"anthropic/claude-sonnet-4-20250514": {
		Provider: "anthropic", Model: "claude-sonnet-4-20250514",
		InputPerToken: 3.0 / 1_000_000, OutputPerToken: 15.0 / 1_000_000,
		DisplayName: "Claude Sonnet 4",
	},
	"anthropic/claude-haiku-3-5": {
		Provider: "anthropic", Model: "claude-haiku-3-5",
		InputPerToken: 0.80 / 1_000_000, OutputPerToken: 4.0 / 1_000_000,
		DisplayName: "Claude Haiku 3.5",
	},

	// OpenAI
	"openai/gpt-4o": {
		Provider: "openai", Model: "gpt-4o",
		InputPerToken: 2.50 / 1_000_000, OutputPerToken: 10.0 / 1_000_000,
		DisplayName: "GPT-4o",
	},
	"openai/gpt-4o-mini": {
		Provider: "openai", Model: "gpt-4o-mini",
		InputPerToken: 0.15 / 1_000_000, OutputPerToken: 0.60 / 1_000_000,
		DisplayName: "GPT-4o Mini",
	},

	// Ollama (local — zero cost)
	"ollama/llama3": {
		Provider: "ollama", Model: "llama3",
		InputPerToken: 0, OutputPerToken: 0,
		DisplayName: "Llama 3 (local)",
	},
	"ollama/llama3.2:1b": {
		Provider: "ollama", Model: "llama3.2:1b",
		InputPerToken: 0, OutputPerToken: 0,
		DisplayName: "Llama 3.2 1B (local)",
	},
	"ollama/llama3.2:3b": {
		Provider: "ollama", Model: "llama3.2:3b",
		InputPerToken: 0, OutputPerToken: 0,
		DisplayName: "Llama 3.2 3B (local)",
	},
}

// ModelPricing returns the pricing for a provider/model pair.
// Falls back to zero-cost if the model is not in the price table.
func ModelPricing(provider, model string) TokenPrice {
	key := strings.ToLower(provider) + "/" + strings.ToLower(model)
	if p, ok := priceTable[key]; ok {
		return p
	}
	// Check prefix matches (e.g. "anthropic/claude-sonnet" matches "anthropic/claude-sonnet-4-...")
	for k, p := range priceTable {
		if strings.HasPrefix(key, k) || strings.HasPrefix(k, key) {
			return p
		}
	}
	// Unknown model — return zero cost with a warning name
	return TokenPrice{
		Provider: provider, Model: model,
		InputPerToken: 0, OutputPerToken: 0,
		DisplayName: fmt.Sprintf("%s/%s (unknown pricing)", provider, model),
	}
}

// CustomPricing creates a TokenPrice with user-specified rates.
func CustomPricing(provider, model string, inputPerMillion, outputPerMillion float64) TokenPrice {
	return TokenPrice{
		Provider:       provider,
		Model:          model,
		InputPerToken:  inputPerMillion / 1_000_000,
		OutputPerToken: outputPerMillion / 1_000_000,
		DisplayName:    fmt.Sprintf("%s/%s (custom)", provider, model),
	}
}

// ──────────────────────────────────────────────────────────
// Attribution — who/what is this cost for?
// ──────────────────────────────────────────────────────────

// Attribution tags a cost record for aggregation.
// All fields are optional — set what you have.
type Attribution struct {
	UserID    string `json:"user_id,omitempty"`
	FeatureID string `json:"feature_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	TeamID    string `json:"team_id,omitempty"`
}

// ──────────────────────────────────────────────────────────
// StepCost — cost of a single decision
// ──────────────────────────────────────────────────────────

// StepCost captures the economic cost of one agent decision.
type StepCost struct {
	Step         int           `json:"step"`
	Action       string        `json:"action"`         // tool_call, reasoning, grounding_rejected, etc.
	ToolName     string        `json:"tool,omitempty"` // which tool was called
	InputTokens  int           `json:"input_tokens"`
	OutputTokens int           `json:"output_tokens"`
	InputCost    float64       `json:"input_cost"`  // USD
	OutputCost   float64       `json:"output_cost"` // USD
	TotalCost    float64       `json:"total_cost"`  // USD
	Latency      time.Duration `json:"latency_ms"`
	CostPerMs    float64       `json:"cost_per_ms"` // efficiency metric
}

// ──────────────────────────────────────────────────────────
// CostReport — the decision cost graph
// ──────────────────────────────────────────────────────────

// CostReport is the complete cost attribution for a task.
// This is the "decision cost graph" — every step has a cost node.
type CostReport struct {
	TaskID         string             `json:"task_id"`
	Model          string             `json:"model"`
	Attribution    Attribution        `json:"attribution,omitempty"`
	Steps          []StepCost         `json:"steps"`
	TotalCost      float64            `json:"total_cost"`
	InputCost      float64            `json:"input_cost"`
	OutputCost     float64            `json:"output_cost"`
	TotalTokens    int                `json:"total_tokens"`
	TotalLatency   time.Duration      `json:"total_latency_ms"`
	BudgetLimit    float64            `json:"budget_limit,omitempty"`
	BudgetUsedPct  float64            `json:"budget_used_pct,omitempty"`
	BudgetExceeded bool               `json:"budget_exceeded"`
	CostByAction   map[string]float64 `json:"cost_by_action"` // action → total cost
	CostByTool     map[string]float64 `json:"cost_by_tool"`   // tool → total cost
}

// ──────────────────────────────────────────────────────────
// Tracker — the core cost engine
// ──────────────────────────────────────────────────────────

// Tracker accumulates per-decision costs during task execution.
// Thread-safe. Create one per task.
type Tracker struct {
	mu          sync.Mutex
	pricing     TokenPrice
	attribution Attribution
	budget      float64 // max USD per task (0 = unlimited)
	steps       []StepCost
	taskID      string
	runningCost float64
}

// NewTracker creates a cost tracker with the given pricing model.
func NewTracker(pricing TokenPrice) *Tracker {
	return &Tracker{
		pricing: pricing,
		steps:   make([]StepCost, 0),
	}
}

// SetBudget sets a maximum cost per task in USD.
// When exceeded, BudgetExceeded() returns true — the runtime should stop.
func (t *Tracker) SetBudget(maxUSD float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.budget = maxUSD
}

// SetAttribution tags this task's costs for aggregation.
func (t *Tracker) SetAttribution(userID, featureID, sessionID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.attribution = Attribution{
		UserID:    userID,
		FeatureID: featureID,
		SessionID: sessionID,
	}
}

// SetTaskID sets the task identifier for the report.
func (t *Tracker) SetTaskID(taskID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.taskID = taskID
}

// RecordStep records the cost of a single decision step.
// inputTokens: tokens sent to the model (prompt + context).
// outputTokens: tokens generated by the model.
// action: "tool_call", "reasoning", "complete", etc.
// toolName: name of tool called (empty if reasoning step).
// latency: wall-clock time for this step.
func (t *Tracker) RecordStep(step int, inputTokens, outputTokens int, action, toolName string, latency time.Duration) StepCost {
	t.mu.Lock()
	defer t.mu.Unlock()

	inputCost := float64(inputTokens) * t.pricing.InputPerToken
	outputCost := float64(outputTokens) * t.pricing.OutputPerToken
	totalCost := inputCost + outputCost

	costPerMs := 0.0
	if latency.Milliseconds() > 0 {
		costPerMs = totalCost / float64(latency.Milliseconds())
	}

	sc := StepCost{
		Step:         step,
		Action:       action,
		ToolName:     toolName,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		InputCost:    inputCost,
		OutputCost:   outputCost,
		TotalCost:    totalCost,
		Latency:      latency,
		CostPerMs:    costPerMs,
	}

	t.steps = append(t.steps, sc)
	t.runningCost += totalCost

	return sc
}

// RunningCost returns the total cost accumulated so far.
func (t *Tracker) RunningCost() float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.runningCost
}

// BudgetExceeded returns true if the running cost exceeds the budget.
// Returns false if no budget is set.
func (t *Tracker) BudgetExceeded() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.budget > 0 && t.runningCost >= t.budget
}

// BudgetRemaining returns the remaining budget in USD.
// Returns -1 if no budget is set.
func (t *Tracker) BudgetRemaining() float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.budget <= 0 {
		return -1
	}
	remaining := t.budget - t.runningCost
	if remaining < 0 {
		return 0
	}
	return remaining
}

// ──────────────────────────────────────────────────────────
// Report — the decision cost graph
// ──────────────────────────────────────────────────────────

// Report generates the complete cost attribution report.
func (t *Tracker) Report() *CostReport {
	t.mu.Lock()
	defer t.mu.Unlock()

	report := &CostReport{
		TaskID:       t.taskID,
		Model:        t.pricing.DisplayName,
		Attribution:  t.attribution,
		Steps:        make([]StepCost, len(t.steps)),
		CostByAction: make(map[string]float64),
		CostByTool:   make(map[string]float64),
	}

	copy(report.Steps, t.steps)

	for _, sc := range t.steps {
		report.TotalCost += sc.TotalCost
		report.InputCost += sc.InputCost
		report.OutputCost += sc.OutputCost
		report.TotalTokens += sc.InputTokens + sc.OutputTokens
		report.TotalLatency += sc.Latency
		report.CostByAction[sc.Action] += sc.TotalCost
		if sc.ToolName != "" {
			report.CostByTool[sc.ToolName] += sc.TotalCost
		}
	}

	if t.budget > 0 {
		report.BudgetLimit = t.budget
		report.BudgetUsedPct = (report.TotalCost / t.budget) * 100
		report.BudgetExceeded = report.TotalCost >= t.budget
	}

	return report
}

// ──────────────────────────────────────────────────────────
// Output Formatting
// ──────────────────────────────────────────────────────────

// JSON exports the cost report as JSON.
func (r *CostReport) JSON() (string, error) {
	data, err := json.MarshalIndent(r, "", "  ")
	return string(data), err
}

// Summary returns a human-readable cost summary for CLI output.
func (r *CostReport) Summary() string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("\n  💰 Cost Report: %s\n", r.TaskID))
	sb.WriteString(fmt.Sprintf("  Model: %s\n", r.Model))

	if r.Attribution.UserID != "" || r.Attribution.FeatureID != "" {
		sb.WriteString("  Attribution:")
		if r.Attribution.UserID != "" {
			sb.WriteString(fmt.Sprintf(" user=%s", r.Attribution.UserID))
		}
		if r.Attribution.FeatureID != "" {
			sb.WriteString(fmt.Sprintf(" feature=%s", r.Attribution.FeatureID))
		}
		if r.Attribution.SessionID != "" {
			sb.WriteString(fmt.Sprintf(" session=%s", r.Attribution.SessionID))
		}
		sb.WriteString("\n")
	}

	sb.WriteString(fmt.Sprintf("  Total Cost: $%.6f\n", r.TotalCost))
	sb.WriteString(fmt.Sprintf("    Input:  $%.6f (%d tokens)\n", r.InputCost, countInputTokens(r.Steps)))
	sb.WriteString(fmt.Sprintf("    Output: $%.6f (%d tokens)\n", r.OutputCost, countOutputTokens(r.Steps)))

	if r.BudgetLimit > 0 {
		sb.WriteString(fmt.Sprintf("  Budget: $%.4f (%.1f%% used)\n", r.BudgetLimit, r.BudgetUsedPct))
		if r.BudgetExceeded {
			sb.WriteString("  ⚠️  BUDGET EXCEEDED\n")
		}
	}

	sb.WriteString("\n  Decision Cost Graph:\n")
	for _, sc := range r.Steps {
		tool := ""
		if sc.ToolName != "" {
			tool = fmt.Sprintf(": %s", sc.ToolName)
		}
		sb.WriteString(fmt.Sprintf("    Step %d [%s%s]  $%.6f  (in:%d out:%d tokens, %v)\n",
			sc.Step, sc.Action, tool, sc.TotalCost,
			sc.InputTokens, sc.OutputTokens,
			sc.Latency.Round(time.Millisecond)))
	}

	if len(r.CostByTool) > 0 {
		sb.WriteString("\n  Cost by Tool:\n")
		for tool, cost := range r.CostByTool {
			sb.WriteString(fmt.Sprintf("    %-30s $%.6f\n", tool, cost))
		}
	}

	if len(r.CostByAction) > 0 {
		sb.WriteString("\n  Cost by Action:\n")
		for action, cost := range r.CostByAction {
			sb.WriteString(fmt.Sprintf("    %-30s $%.6f\n", action, cost))
		}
	}

	return sb.String()
}

func countInputTokens(steps []StepCost) int {
	total := 0
	for _, s := range steps {
		total += s.InputTokens
	}
	return total
}

func countOutputTokens(steps []StepCost) int {
	total := 0
	for _, s := range steps {
		total += s.OutputTokens
	}
	return total
}

// ──────────────────────────────────────────────────────────
// Aggregate — cross-task cost aggregation
// ──────────────────────────────────────────────────────────

// Aggregate collects cost reports across multiple tasks for analysis.
// Thread-safe. Typically one per agent lifetime.
type Aggregate struct {
	mu      sync.Mutex
	reports []*CostReport
}

// NewAggregate creates a cost aggregator.
func NewAggregate() *Aggregate {
	return &Aggregate{reports: make([]*CostReport, 0)}
}

// Add records a completed task's cost report.
func (a *Aggregate) Add(report *CostReport) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reports = append(a.reports, report)
}

// TotalCost returns the sum of all recorded task costs.
func (a *Aggregate) TotalCost() float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	total := 0.0
	for _, r := range a.reports {
		total += r.TotalCost
	}
	return total
}

// CostByFeature returns total cost grouped by feature_id.
func (a *Aggregate) CostByFeature() map[string]float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := make(map[string]float64)
	for _, r := range a.reports {
		if r.Attribution.FeatureID != "" {
			result[r.Attribution.FeatureID] += r.TotalCost
		}
	}
	return result
}

// CostByUser returns total cost grouped by user_id.
func (a *Aggregate) CostByUser() map[string]float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := make(map[string]float64)
	for _, r := range a.reports {
		if r.Attribution.UserID != "" {
			result[r.Attribution.UserID] += r.TotalCost
		}
	}
	return result
}

// CostByTool returns total cost grouped by tool name across all tasks.
func (a *Aggregate) CostByTool() map[string]float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := make(map[string]float64)
	for _, r := range a.reports {
		for tool, cost := range r.CostByTool {
			result[tool] += cost
		}
	}
	return result
}

// TaskCount returns the number of recorded tasks.
func (a *Aggregate) TaskCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.reports)
}

// AvgCostPerTask returns the average cost per task.
func (a *Aggregate) AvgCostPerTask() float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.reports) == 0 {
		return 0
	}
	total := 0.0
	for _, r := range a.reports {
		total += r.TotalCost
	}
	return total / float64(len(a.reports))
}

// ToolCostEfficiency returns cost-per-success for each tool.
// Lower is better. Tools that cost a lot but fail often will score high.
// This feeds directly into ARK's ranking system as a cost signal.
func (a *Aggregate) ToolCostEfficiency() map[string]float64 {
	a.mu.Lock()
	defer a.mu.Unlock()

	toolCost := make(map[string]float64)
	toolSuccess := make(map[string]int)

	for _, r := range a.reports {
		for _, sc := range r.Steps {
			if sc.ToolName == "" {
				continue
			}
			toolCost[sc.ToolName] += sc.TotalCost
			if sc.Action == "tool_call" { // success
				toolSuccess[sc.ToolName]++
			}
		}
	}

	result := make(map[string]float64)
	for tool, cost := range toolCost {
		successes := toolSuccess[tool]
		if successes > 0 {
			result[tool] = cost / float64(successes) // cost per successful call
		} else {
			result[tool] = cost // all cost, zero success — worst efficiency
		}
	}
	return result
}
