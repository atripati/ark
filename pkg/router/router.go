package router

import (
	"fmt"
	"sync"
	"time"

	"github.com/atripati/ark/pkg/runtime"
)

type StepType string

const (
	StepToolCall  StepType = "tool_call"
	StepComplete  StepType = "complete"
	StepRetry     StepType = "retry"
	StepGrounding StepType = "grounding"
)

type Tier string

const (
	TierFast   Tier = "fast"
	TierStrong Tier = "strong"
)

type Strategy string

const (
	StrategySingle        Strategy = "single"
	StrategyCostOptimized Strategy = "cost_optimized"
	StrategyQualityFirst  Strategy = "quality_first"
)

type Decision struct {
	Step       int      `json:"step"`
	StepType   StepType `json:"step_type"`
	Tier       Tier     `json:"tier"`
	ModelUsed  string   `json:"model_used"`
	Reason     string   `json:"reason"`
	Fallback   bool     `json:"fallback"`
	PriorModel string   `json:"prior_model,omitempty"`
}

type Config struct {
	Strategy    Strategy
	FastModel   ModelSpec
	StrongModel ModelSpec
}

type ModelSpec struct {
	Provider  string
	Name      string
	APIKey    string
	BaseURL   string
	MaxTokens int
}

func DefaultConfig() Config {
	return Config{
		Strategy: StrategySingle,
	}
}

type stepPerf struct {
	Attempts   int
	Successes  int
	Failures   int
	AvgLatency time.Duration
}

func (sp *stepPerf) successRate() float64 {
	if sp.Attempts == 0 {
		return 0.5 // no data = neutral
	}
	return float64(sp.Successes) / float64(sp.Attempts)
}

type Router struct {
	mu     sync.Mutex
	config Config
	fast   runtime.Executor // cheap model
	strong runtime.Executor // expensive model
	single runtime.Executor // single-mode fallback

	perf map[string]*stepPerf

	currentStep     int
	currentStepType StepType

	// Decision log for the current task
	decisions []Decision

	promotionThreshold float64
}

func New(cfg Config, fast, strong runtime.Executor) *Router {
	r := &Router{
		config:             cfg,
		fast:               fast,
		strong:             strong,
		perf:               make(map[string]*stepPerf),
		decisions:          make([]Decision, 0),
		promotionThreshold: 0.60,
	}

	if cfg.Strategy == StrategySingle {
		r.single = fast
	}

	return r
}

func NewSingle(executor runtime.Executor) *Router {
	return &Router{
		config:    Config{Strategy: StrategySingle},
		single:    executor,
		fast:      executor,
		strong:    executor,
		perf:      make(map[string]*stepPerf),
		decisions: make([]Decision, 0),
	}
}

func (r *Router) SetStep(step int, stepType string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.currentStep = step
	r.currentStepType = StepType(stepType)
}

func ClassifyStep(step int, isRetry bool, toolCallsCompleted int, hasToolResult bool) StepType {
	if isRetry {
		return StepRetry
	}
	if toolCallsCompleted == 0 && !hasToolResult {
		return StepToolCall
	}
	if hasToolResult {
		return StepComplete
	}
	return StepToolCall
}

func (r *Router) Execute(context string, task string) (*runtime.ModelResponse, error) {
	r.mu.Lock()
	step := r.currentStep
	stepType := r.currentStepType
	strategy := r.config.Strategy
	r.mu.Unlock()

	switch strategy {
	case StrategySingle:
		return r.executeSingle(context, task, step, stepType)
	case StrategyQualityFirst:
		return r.executeQualityFirst(context, task, step, stepType)
	case StrategyCostOptimized:
		return r.executeCostOptimized(context, task, step, stepType)
	default:
		return r.executeSingle(context, task, step, stepType)
	}
}

func (r *Router) executeSingle(context, task string, step int, stepType StepType) (*runtime.ModelResponse, error) {
	r.recordDecision(Decision{
		Step:      step,
		StepType:  stepType,
		Tier:      TierFast,
		ModelUsed: r.modelName(TierFast),
		Reason:    "strategy=single, using configured model",
	})

	resp, err := r.single.Execute(context, task)
	r.recordPerf(stepType, TierFast, err == nil, resp)
	return resp, err
}

func (r *Router) executeQualityFirst(context, task string, step int, stepType StepType) (*runtime.ModelResponse, error) {
	r.recordDecision(Decision{
		Step:      step,
		StepType:  stepType,
		Tier:      TierStrong,
		ModelUsed: r.modelName(TierStrong),
		Reason:    "strategy=quality_first, always using strong model",
	})

	resp, err := r.strong.Execute(context, task)
	r.recordPerf(stepType, TierStrong, err == nil, resp)
	return resp, err
}

func (r *Router) executeCostOptimized(context, task string, step int, stepType StepType) (*runtime.ModelResponse, error) {
	tier := r.selectTier(stepType)
	executor := r.executorForTier(tier)
	modelName := r.modelName(tier)

	reason := r.explainChoice(stepType, tier)

	r.recordDecision(Decision{
		Step:      step,
		StepType:  stepType,
		Tier:      tier,
		ModelUsed: modelName,
		Reason:    reason,
	})

	resp, err := executor.Execute(context, task)

	if err != nil && tier == TierFast {
		r.recordPerf(stepType, TierFast, false, nil)

		strongName := r.modelName(TierStrong)
		r.recordDecision(Decision{
			Step:       step,
			StepType:   stepType,
			Tier:       TierStrong,
			ModelUsed:  strongName,
			Reason:     fmt.Sprintf("fallback: %s failed, retrying with %s", modelName, strongName),
			Fallback:   true,
			PriorModel: modelName,
		})

		resp, err = r.strong.Execute(context, task)
		r.recordPerf(stepType, TierStrong, err == nil, resp)
		return resp, err
	}

	r.recordPerf(stepType, tier, err == nil, resp)
	return resp, err
}

func (r *Router) selectTier(stepType StepType) Tier {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Rule 1: retries always need the strong model
	if stepType == StepRetry {
		return TierStrong
	}

	// Rule 2: check if fast model has been failing for this step type
	fastKey := perfKey(stepType, TierFast)
	if perf, ok := r.perf[fastKey]; ok {
		if perf.Attempts >= 2 && perf.successRate() < r.promotionThreshold {
			return TierStrong // fast model not reliable for this step type
		}
	}

	// Rule 3: final reasoning/summary needs quality
	if stepType == StepComplete {
		return TierStrong
	}

	// Rule 4: everything else → fast
	return TierFast
}

// explainChoice generates a human-readable reason for the routing decision.
func (r *Router) explainChoice(stepType StepType, tier Tier) string {
	r.mu.Lock()
	defer r.mu.Unlock()

	switch {
	case stepType == StepRetry:
		return "retry/error recovery requires strong reasoning"

	case stepType == StepComplete && tier == TierStrong:
		return "final reasoning/summary benefits from strong model"

	case tier == TierStrong:
		fastKey := perfKey(stepType, TierFast)
		if perf, ok := r.perf[fastKey]; ok && perf.successRate() < r.promotionThreshold {
			return fmt.Sprintf("promoted: fast model has %.0f%% success rate for %s (threshold: %.0f%%)",
				perf.successRate()*100, stepType, r.promotionThreshold*100)
		}
		return "strong model selected for quality"

	case stepType == StepToolCall && tier == TierFast:
		return "tool calls are simple, using fast model to save cost"

	case stepType == StepGrounding && tier == TierFast:
		return "grounding checks are simple, using fast model"

	default:
		return fmt.Sprintf("step_type=%s → tier=%s", stepType, tier)
	}
}

// this is for performance recording i mean self improving

func (r *Router) recordPerf(stepType StepType, tier Tier, success bool, resp *runtime.ModelResponse) {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := perfKey(stepType, tier)
	perf, ok := r.perf[key]
	if !ok {
		perf = &stepPerf{}
		r.perf[key] = perf
	}

	perf.Attempts++
	if success {
		perf.Successes++
	} else {
		perf.Failures++
	}

	if resp != nil {
		// Exponential moving average for latency
		if perf.AvgLatency == 0 {
			perf.AvgLatency = resp.Latency
		} else {
			perf.AvgLatency = time.Duration(float64(perf.AvgLatency)*0.7 + float64(resp.Latency)*0.3)
		}
	}
}

func (r *Router) recordDecision(d Decision) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.decisions = append(r.decisions, d)
}

func (r *Router) Decisions() []Decision {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Decision, len(r.decisions))
	copy(out, r.decisions)
	return out
}

func (r *Router) ResetDecisions() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.decisions = make([]Decision, 0)
}

func (r *Router) Stats() map[string]StepPerfStats {
	r.mu.Lock()
	defer r.mu.Unlock()

	result := make(map[string]StepPerfStats)
	for key, perf := range r.perf {
		result[key] = StepPerfStats{
			Attempts:    perf.Attempts,
			Successes:   perf.Successes,
			Failures:    perf.Failures,
			SuccessRate: perf.successRate(),
			AvgLatency:  perf.AvgLatency,
		}
	}
	return result
}

type StepPerfStats struct {
	Attempts    int           `json:"attempts"`
	Successes   int           `json:"successes"`
	Failures    int           `json:"failures"`
	SuccessRate float64       `json:"success_rate"`
	AvgLatency  time.Duration `json:"avg_latency"`
}

func (r *Router) Strategy() Strategy {
	return r.config.Strategy
}

func (r *Router) FormatDecisions() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.decisions) == 0 {
		return ""
	}

	result := "\n  🧠 Model Routing:\n"

	fastSteps := 0
	strongSteps := 0
	fallbacks := 0

	for _, d := range r.decisions {
		if d.Fallback {
			fallbacks++
			result += fmt.Sprintf("    Step %d [%s] %s → FAILED → %s (%s)\n",
				d.Step, d.StepType, d.PriorModel, d.ModelUsed, d.Reason)
		} else {
			result += fmt.Sprintf("    Step %d [%s] %s (%s)\n",
				d.Step, d.StepType, d.ModelUsed, d.Reason)
		}

		switch d.Tier {
		case TierFast:
			fastSteps++
		case TierStrong:
			strongSteps++
		}
	}

	if fastSteps > 0 || strongSteps > 0 {
		result += fmt.Sprintf("\n    Fast model: %d steps | Strong model: %d steps", fastSteps, strongSteps)
		if fallbacks > 0 {
			result += fmt.Sprintf(" | Fallbacks: %d", fallbacks)
		}
		result += "\n"
	}

	return result
}

func (r *Router) executorForTier(tier Tier) runtime.Executor {
	if tier == TierStrong {
		return r.strong
	}
	return r.fast
}

func (r *Router) modelName(tier Tier) string {
	switch tier {
	case TierFast:
		if r.config.FastModel.Name != "" {
			return r.config.FastModel.Name
		}
		return "fast"
	case TierStrong:
		if r.config.StrongModel.Name != "" {
			return r.config.StrongModel.Name
		}
		return "strong"
	default:
		return "unknown"
	}
}

func perfKey(stepType StepType, tier Tier) string {
	return string(stepType) + ":" + string(tier)
}

type PerfSnapshot struct {
	Key         string  `json:"key"`
	Attempts    int     `json:"attempts"`
	Successes   int     `json:"successes"`
	Failures    int     `json:"failures"`
	SuccessRate float64 `json:"success_rate"`
}

func (r *Router) ExportLearning() []PerfSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()

	snapshots := make([]PerfSnapshot, 0, len(r.perf))
	for key, perf := range r.perf {
		snapshots = append(snapshots, PerfSnapshot{
			Key:         key,
			Attempts:    perf.Attempts,
			Successes:   perf.Successes,
			Failures:    perf.Failures,
			SuccessRate: perf.successRate(),
		})
	}
	return snapshots
}
func (r *Router) ImportLearning(snapshots []PerfSnapshot) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, snap := range snapshots {
		r.perf[snap.Key] = &stepPerf{
			Attempts:  snap.Attempts,
			Successes: snap.Successes,
			Failures:  snap.Failures,
		}
	}
}
