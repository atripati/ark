package cost

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

type TokenPrice struct {
	Provider       string
	Model          string
	InputPerToken  float64
	OutputPerToken float64
	DisplayName    string
}

var priceTable = map[string]TokenPrice{
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

// lookupPricing reports whether the model is actually known to the price table.
// This distinction matters: an unknown model must NOT silently price at zero, or
// cost is understated. Callers that have a fallback should use this and prefer
// the fallback when ok is false.
func lookupPricing(provider, model string) (TokenPrice, bool) {
	key := strings.ToLower(provider) + "/" + strings.ToLower(model)
	if p, ok := priceTable[key]; ok {
		return p, true
	}
	// Deterministic resolution — never depend on map iteration order. A versioned or
	// snapshot name can prefix-match several base entries (e.g. gpt-4o-mini-2024-07-18
	// matches both gpt-4o and gpt-4o-mini). Prefer the LONGEST base entry that is a
	// segment-boundary prefix of the queried name, so a snapshot resolves to its own base
	// model, never a shorter sibling.
	best, bestLen := "", -1
	for k := range priceTable {
		if isModelPrefix(k, key) && len(k) > bestLen {
			best, bestLen = k, len(k)
		}
	}
	if bestLen >= 0 {
		return priceTable[best], true
	}
	// Reverse: a base name may map to a single snapshot entry (e.g. claude-sonnet-4 ->
	// claude-sonnet-4-20250514). Pick the lexicographically smallest match so the result
	// is stable regardless of map order.
	rev := ""
	for k := range priceTable {
		if isModelPrefix(key, k) && (rev == "" || k < rev) {
			rev = k
		}
	}
	if rev != "" {
		return priceTable[rev], true
	}
	return TokenPrice{}, false
}

// isModelPrefix reports whether short is a segment-boundary prefix of full: they are equal,
// or full continues after short at a version boundary ('-', ':', '/'). The boundary check
// stops gpt-4o from matching gpt-4o-mini, and llama3 from matching llama3.2.
func isModelPrefix(short, full string) bool {
	if short == full {
		return true
	}
	if !strings.HasPrefix(full, short) {
		return false
	}
	switch full[len(short)] {
	case '-', ':', '/':
		return true
	default:
		return false
	}
}

func ModelPricing(provider, model string) TokenPrice {
	if p, ok := lookupPricing(provider, model); ok {
		return p
	}
	return TokenPrice{
		Provider: provider, Model: model,
		InputPerToken: 0, OutputPerToken: 0,
		DisplayName: fmt.Sprintf("%s/%s (unknown pricing)", provider, model),
	}
}

func CustomPricing(provider, model string, inputPerMillion, outputPerMillion float64) TokenPrice {
	return TokenPrice{
		Provider:       provider,
		Model:          model,
		InputPerToken:  inputPerMillion / 1_000_000,
		OutputPerToken: outputPerMillion / 1_000_000,
		DisplayName:    fmt.Sprintf("%s/%s (custom)", provider, model),
	}
}

type Attribution struct {
	UserID    string `json:"user_id,omitempty"`
	FeatureID string `json:"feature_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	TeamID    string `json:"team_id,omitempty"`
}

type StepCost struct {
	Step         int           `json:"step"`
	Action       string        `json:"action"`
	ToolName     string        `json:"tool,omitempty"`
	InputTokens  int           `json:"input_tokens"`
	OutputTokens int           `json:"output_tokens"`
	InputCost    float64       `json:"input_cost"`  // US dollar
	OutputCost   float64       `json:"output_cost"` // US dollar
	TotalCost    float64       `json:"total_cost"`  // US dollar
	Latency      time.Duration `json:"latency_ms"`
	CostPerMs    float64       `json:"cost_per_ms"`
	Model        string        `json:"model,omitempty"` // model this step was priced against
}

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
	CostByAction   map[string]float64 `json:"cost_by_action"`
	CostByTool     map[string]float64 `json:"cost_by_tool"`
}

type Tracker struct {
	mu          sync.Mutex
	pricing     TokenPrice
	attribution Attribution
	budget      float64
	steps       []StepCost
	taskID      string
	runningCost float64
}

func NewTracker(pricing TokenPrice) *Tracker {
	return &Tracker{
		pricing: pricing,
		steps:   make([]StepCost, 0),
	}
}

func (t *Tracker) SetBudget(maxUSD float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.budget = maxUSD
}

func (t *Tracker) SetAttribution(userID, featureID, sessionID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.attribution = Attribution{
		UserID:    userID,
		FeatureID: featureID,
		SessionID: sessionID,
	}
}

func (t *Tracker) SetTaskID(taskID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.taskID = taskID
}

// RecordStep records a step priced at the tracker's default (configured) model.
// Prefer RecordStepWithModel when the model that actually ran the step is known:
// with per-step routing the configured model is frequently NOT the model used,
// and pricing every step at the configured rate misreports cost — usually by
// overstating it, which hides the savings routing actually produced.
func (t *Tracker) RecordStep(step int, inputTokens, outputTokens int, action, toolName string, latency time.Duration) StepCost {
	return t.RecordStepWithModel(step, inputTokens, outputTokens, action, toolName, latency, "", "")
}

// RecordStepWithModel records a step priced at the model that actually executed
// it. An empty model falls back to the tracker's configured pricing.
func (t *Tracker) RecordStepWithModel(step int, inputTokens, outputTokens int, action, toolName string, latency time.Duration, provider, model string) StepCost {
	t.mu.Lock()
	defer t.mu.Unlock()

	pricing := t.pricing
	if model != "" {
		if p, ok := lookupPricing(provider, model); ok {
			pricing = p
		}
	}

	inputCost := float64(inputTokens) * pricing.InputPerToken
	outputCost := float64(outputTokens) * pricing.OutputPerToken
	totalCost := inputCost + outputCost

	costPerMs := 0.0
	if latency.Milliseconds() > 0 {
		costPerMs = totalCost / float64(latency.Milliseconds())
	}

	// A step that consumed no tokens involved no model call — a refusal recorded
	// before the loop, for instance. Stamping a model name on it would manufacture
	// an attribution for a call that never happened, and every downstream label is
	// derived from this field.
	stepModel := ""
	if inputTokens > 0 || outputTokens > 0 {
		stepModel = pricing.DisplayName
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
		Model:        stepModel,
	}

	t.steps = append(t.steps, sc)
	t.runningCost += totalCost

	return sc
}

func (t *Tracker) RunningCost() float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.runningCost
}

func (t *Tracker) BudgetExceeded() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.budget > 0 && t.runningCost >= t.budget
}

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

// modelLabel names the model(s) the task actually ran on. With per-step routing
// a task commonly spans two models; reporting only the configured one would
// misdescribe what happened. Caller must hold t.mu.
func (t *Tracker) modelLabel() string {
	seen := make(map[string]bool)
	var models []string
	for _, sc := range t.steps {
		// A step that consumed no tokens involved no model call and must not
		// contribute a model name. This matters because a run refused before any
		// call still records one step explaining the refusal, and because every
		// step carries a Model field regardless — it is set from whichever
		// pricing was applied, falling back to the configured model. So the
		// presence of a model name is not evidence a model ran; token usage is.
		if sc.InputTokens == 0 && sc.OutputTokens == 0 {
			continue
		}
		if sc.Model != "" && !seen[sc.Model] {
			seen[sc.Model] = true
			models = append(models, sc.Model)
		}
	}
	switch len(models) {
	case 0:
		return "none"
	case 1:
		return models[0]
	default:
		return strings.Join(models, " + ") + " (routed)"
	}
}

func (t *Tracker) Report() *CostReport {
	t.mu.Lock()
	defer t.mu.Unlock()

	report := &CostReport{
		TaskID:       t.taskID,
		Model:        t.modelLabel(),
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

func (r *CostReport) JSON() (string, error) {
	data, err := json.MarshalIndent(r, "", "  ")
	return string(data), err
}

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

type Aggregate struct {
	mu      sync.Mutex
	reports []*CostReport
}

func NewAggregate() *Aggregate {
	return &Aggregate{reports: make([]*CostReport, 0)}
}

func (a *Aggregate) Add(report *CostReport) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reports = append(a.reports, report)
}

func (a *Aggregate) TotalCost() float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	total := 0.0
	for _, r := range a.reports {
		total += r.TotalCost
	}
	return total
}

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

func (a *Aggregate) TaskCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.reports)
}

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
			if sc.Action == "tool_call" {
				toolSuccess[sc.ToolName]++
			}
		}
	}

	result := make(map[string]float64)
	for tool, cost := range toolCost {
		successes := toolSuccess[tool]
		if successes > 0 {
			result[tool] = cost / float64(successes)
		} else {
			result[tool] = cost
		}
	}
	return result
}
