package governor

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sync"
	"time"
)

type ModelProfile struct {
	ModelName string `json:"model_name"`

	// Aggregate stats
	TotalCalls   int     `json:"total_calls"`
	TotalSuccess int     `json:"total_success"`
	TotalFailure int     `json:"total_failure"`
	SuccessRate  float64 `json:"success_rate"`

	// Capability scores (0.0 to 1.0, updated via Bayesian learning)
	ToolCallScore  float64 `json:"tool_call_score"`
	ReasoningScore float64 `json:"reasoning_score"`
	CodingScore    float64 `json:"coding_score"`
	FollowScore    float64 `json:"follow_score"`

	// Performance
	AvgLatencyMs float64 `json:"avg_latency_ms"`
	AvgCost      float64 `json:"avg_cost"`

	// Reliability
	HallucinationCount int     `json:"hallucination_count"`
	HallucinationRate  float64 `json:"hallucination_rate"`

	// Per-tool track record: tool_name → ToolRecord
	ToolRecords map[string]*ToolRecord `json:"tool_records"`

	// Per-step-type track record: step_type → StepRecord
	StepRecords map[string]*StepTypeRecord `json:"step_records"`

	// Failure patterns: error_class → count
	FailurePatterns map[string]int `json:"failure_patterns"`

	LastUpdated time.Time `json:"last_updated"`
}

// ToolRecord tracks how a specific model performs with a specific tool.
type ToolRecord struct {
	Calls       int       `json:"calls"`
	Successes   int       `json:"successes"`
	Failures    int       `json:"failures"`
	SuccessRate float64   `json:"success_rate"`
	AvgLatency  float64   `json:"avg_latency_ms"`
	LastFailure time.Time `json:"last_failure,omitempty"`
	LastSuccess time.Time `json:"last_success,omitempty"`
}

// StepTypeRecord tracks how a model performs for a specific step type.
type StepTypeRecord struct {
	Calls       int     `json:"calls"`
	Successes   int     `json:"successes"`
	Failures    int     `json:"failures"`
	SuccessRate float64 `json:"success_rate"`
}

// ObservationKind describes what was observed.
type ObservationKind string

const (
	ObsToolCallSuccess    ObservationKind = "tool_call_success"
	ObsToolCallFailure    ObservationKind = "tool_call_failure"
	ObsReasoningSuccess   ObservationKind = "reasoning_success"
	ObsReasoningFailure   ObservationKind = "reasoning_failure"
	ObsHallucination      ObservationKind = "hallucination"
	ObsVerificationPassed ObservationKind = "verification_passed"
	ObsVerificationFailed ObservationKind = "verification_failed"
)

// Observation is a single data point the registry records.
type Observation struct {
	Model      string
	Kind       ObservationKind
	StepType   string
	ToolName   string
	LatencyMs  float64
	Cost       float64
	ErrorClass string
}

// Registry is the Model Capability Registry — the Teacher's knowledge of every student.
type Registry struct {
	mu       sync.RWMutex
	profiles map[string]*ModelProfile
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		profiles: make(map[string]*ModelProfile),
	}
}

func (r *Registry) getOrCreate(model string) *ModelProfile {
	p, ok := r.profiles[model]
	if !ok {
		p = &ModelProfile{
			ModelName:       model,
			ToolCallScore:   0.5, // neutral prior
			ReasoningScore:  0.5,
			CodingScore:     0.5,
			FollowScore:     0.5,
			ToolRecords:     make(map[string]*ToolRecord),
			StepRecords:     make(map[string]*StepTypeRecord),
			FailurePatterns: make(map[string]int),
		}
		r.profiles[model] = p
	}
	return p
}

// Record records an observation and updates the model's profile.
func (r *Registry) Record(obs Observation) {
	r.mu.Lock()
	defer r.mu.Unlock()

	p := r.getOrCreate(obs.Model)
	p.TotalCalls++
	p.LastUpdated = time.Now()

	// Update aggregate success/failure
	switch obs.Kind {
	case ObsToolCallSuccess, ObsReasoningSuccess, ObsVerificationPassed:
		p.TotalSuccess++
	case ObsToolCallFailure, ObsReasoningFailure, ObsHallucination, ObsVerificationFailed:
		p.TotalFailure++
	}

	if p.TotalCalls > 0 {
		p.SuccessRate = float64(p.TotalSuccess) / float64(p.TotalCalls)
	}

	// Update latency (exponential moving average)
	if obs.LatencyMs > 0 {
		if p.AvgLatencyMs == 0 {
			p.AvgLatencyMs = obs.LatencyMs
		} else {
			p.AvgLatencyMs = p.AvgLatencyMs*0.8 + obs.LatencyMs*0.2
		}
	}

	// Update cost (exponential moving average)
	if obs.Cost > 0 {
		if p.AvgCost == 0 {
			p.AvgCost = obs.Cost
		} else {
			p.AvgCost = p.AvgCost*0.8 + obs.Cost*0.2
		}
	}

	// Update capability scores (Bayesian-style update)
	r.updateCapabilityScores(p, obs)

	// Update per-tool records
	if obs.ToolName != "" {
		r.updateToolRecord(p, obs)
	}

	// Update per-step-type records
	if obs.StepType != "" {
		r.updateStepRecord(p, obs)
	}

	// Record failure patterns
	if obs.ErrorClass != "" {
		p.FailurePatterns[obs.ErrorClass]++
	}

	// Track hallucinations
	if obs.Kind == ObsHallucination {
		p.HallucinationCount++
		if p.TotalCalls > 0 {
			p.HallucinationRate = float64(p.HallucinationCount) / float64(p.TotalCalls)
		}
	}
}

func (r *Registry) updateCapabilityScores(p *ModelProfile, obs Observation) {
	// Learning rate decays as we get more data (more confident)
	lr := 1.0 / math.Max(float64(p.TotalCalls), 5.0)

	switch obs.Kind {
	case ObsToolCallSuccess:
		p.ToolCallScore = clamp(p.ToolCallScore + lr)
		p.FollowScore = clamp(p.FollowScore + lr*0.5)
	case ObsToolCallFailure:
		p.ToolCallScore = clamp(p.ToolCallScore - lr)
	case ObsReasoningSuccess:
		p.ReasoningScore = clamp(p.ReasoningScore + lr)
	case ObsReasoningFailure:
		p.ReasoningScore = clamp(p.ReasoningScore - lr)
	case ObsHallucination:
		p.ReasoningScore = clamp(p.ReasoningScore - lr*2)
		p.FollowScore = clamp(p.FollowScore - lr)
	case ObsVerificationPassed:
		p.FollowScore = clamp(p.FollowScore + lr*0.3)
	case ObsVerificationFailed:
		p.FollowScore = clamp(p.FollowScore - lr*0.5)
	}
}

func (r *Registry) updateToolRecord(p *ModelProfile, obs Observation) {
	tr, ok := p.ToolRecords[obs.ToolName]
	if !ok {
		tr = &ToolRecord{}
		p.ToolRecords[obs.ToolName] = tr
	}

	tr.Calls++
	now := time.Now()

	switch obs.Kind {
	case ObsToolCallSuccess, ObsVerificationPassed:
		tr.Successes++
		tr.LastSuccess = now
	case ObsToolCallFailure, ObsHallucination, ObsVerificationFailed:
		tr.Failures++
		tr.LastFailure = now
	}

	if tr.Calls > 0 {
		tr.SuccessRate = float64(tr.Successes) / float64(tr.Calls)
	}

	if obs.LatencyMs > 0 {
		if tr.AvgLatency == 0 {
			tr.AvgLatency = obs.LatencyMs
		} else {
			tr.AvgLatency = tr.AvgLatency*0.7 + obs.LatencyMs*0.3
		}
	}
}

func (r *Registry) updateStepRecord(p *ModelProfile, obs Observation) {
	sr, ok := p.StepRecords[obs.StepType]
	if !ok {
		sr = &StepTypeRecord{}
		p.StepRecords[obs.StepType] = sr
	}

	sr.Calls++
	switch obs.Kind {
	case ObsToolCallSuccess, ObsReasoningSuccess, ObsVerificationPassed:
		sr.Successes++
	case ObsToolCallFailure, ObsReasoningFailure, ObsHallucination, ObsVerificationFailed:
		sr.Failures++
	}

	if sr.Calls > 0 {
		sr.SuccessRate = float64(sr.Successes) / float64(sr.Calls)
	}
}

func (r *Registry) BestModelFor(stepType string, toolName string, candidates []string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	bestModel := ""
	bestScore := -1.0

	for _, model := range candidates {
		score := r.scoreModelFor(model, stepType, toolName)
		if score > bestScore {
			bestScore = score
			bestModel = model
		}
	}

	return bestModel
}

// scoreModelFor computes a composite score for how well a model handles a specific context.
func (r *Registry) scoreModelFor(model, stepType, toolName string) float64 {
	p, ok := r.profiles[model]
	if !ok {
		return 0.5 // no data = neutral
	}

	score := 0.0

	// 40% weight: step-type specific success rate
	if sr, ok := p.StepRecords[stepType]; ok && sr.Calls >= 2 {
		score += sr.SuccessRate * 0.4
	} else {
		score += p.SuccessRate * 0.4 // fallback to aggregate
	}

	// 25% weight: tool-specific success rate (if tool provided)
	if toolName != "" {
		if tr, ok := p.ToolRecords[toolName]; ok && tr.Calls >= 2 {
			score += tr.SuccessRate * 0.25
		} else {
			score += p.ToolCallScore * 0.25
		}
	} else {
		score += p.ReasoningScore * 0.25
	}

	// 15% weight: hallucination penalty (direct subtraction)
	score -= p.HallucinationRate * 0.15

	// 10% weight: instruction following
	score += p.FollowScore * 0.10

	// 10% weight: cost (relative to cheapest known model)
	if p.AvgCost > 0 {
		minCost := r.minModelCost()
		if minCost > 0 {
			score += (minCost / p.AvgCost) * 0.10
		} else {
			score += 0.05
		}
	} else {
		score += 0.10 // free model gets full cost score
	}

	return score
}

func (r *Registry) ShouldDemote(model, toolName string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	p, ok := r.profiles[model]
	if !ok {
		return false
	}

	tr, ok := p.ToolRecords[toolName]
	if !ok {
		return false
	}

	return tr.Calls >= 3 && tr.SuccessRate < 0.5
}

// GetProfile returns a copy of a model's profile.
func (r *Registry) GetProfile(model string) *ModelProfile {
	r.mu.RLock()
	defer r.mu.RUnlock()

	p, ok := r.profiles[model]
	if !ok {
		return nil
	}

	// Return a copy
	cp := *p
	cp.ToolRecords = make(map[string]*ToolRecord)
	for k, v := range p.ToolRecords {
		vv := *v
		cp.ToolRecords[k] = &vv
	}
	cp.StepRecords = make(map[string]*StepTypeRecord)
	for k, v := range p.StepRecords {
		vv := *v
		cp.StepRecords[k] = &vv
	}
	cp.FailurePatterns = make(map[string]int)
	for k, v := range p.FailurePatterns {
		cp.FailurePatterns[k] = v
	}
	return &cp
}

// AllProfiles returns profiles for all known models.
func (r *Registry) AllProfiles() map[string]*ModelProfile {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make(map[string]*ModelProfile, len(r.profiles))
	for name := range r.profiles {
		result[name] = r.GetProfile(name)
	}
	return result
}

// FormatReport generates a human-readable report of all model capabilities.
func (r *Registry) FormatReport() string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(r.profiles) == 0 {
		return "  No model data yet.\n"
	}

	result := "\n  📋 Model Capability Registry:\n"

	for name, p := range r.profiles {
		result += fmt.Sprintf("\n  ┌─ %s\n", name)
		result += fmt.Sprintf("  │  Calls: %d (✓ %d  ✗ %d) — %.0f%% success\n",
			p.TotalCalls, p.TotalSuccess, p.TotalFailure, p.SuccessRate*100)
		result += fmt.Sprintf("  │  Scores: tool=%.2f  reasoning=%.2f  follow=%.2f\n",
			p.ToolCallScore, p.ReasoningScore, p.FollowScore)

		if p.HallucinationCount > 0 {
			result += fmt.Sprintf("  │  ⚠️  Hallucinations: %d (%.1f%% rate)\n",
				p.HallucinationCount, p.HallucinationRate*100)
		}

		if p.AvgLatencyMs > 0 {
			result += fmt.Sprintf("  │  Avg latency: %.0fms  Avg cost: $%.6f\n",
				p.AvgLatencyMs, p.AvgCost)
		}

		// Tool-specific records
		if len(p.ToolRecords) > 0 {
			result += "  │  Tools:\n"
			for tool, tr := range p.ToolRecords {
				status := "✓"
				if tr.SuccessRate < 0.5 && tr.Calls >= 3 {
					status = "⚠️"
				}
				result += fmt.Sprintf("  │    %s %-25s %d calls, %.0f%% success\n",
					status, tool, tr.Calls, tr.SuccessRate*100)
			}
		}

		// Failure patterns
		if len(p.FailurePatterns) > 0 {
			result += "  │  Failure patterns:\n"
			for pattern, count := range p.FailurePatterns {
				result += fmt.Sprintf("  │    %-25s %d occurrences\n", pattern, count)
			}
		}

		result += "  └─\n"
	}

	return result
}

func (r *Registry) DecayProfiles(maxAge time.Duration) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	decayed := 0
	cutoff := time.Now().Add(-maxAge)

	for _, p := range r.profiles {
		if p.LastUpdated.Before(cutoff) && p.TotalCalls > 2 {
			// Halve all counts — recent data will dominate after new observations
			p.TotalCalls = p.TotalCalls / 2
			p.TotalSuccess = p.TotalSuccess / 2
			p.TotalFailure = p.TotalFailure / 2
			if p.TotalCalls > 0 {
				p.SuccessRate = float64(p.TotalSuccess) / float64(p.TotalCalls)
			}

			for _, tr := range p.ToolRecords {
				if tr.Calls > 2 {
					tr.Calls = tr.Calls / 2
					tr.Successes = tr.Successes / 2
					tr.Failures = tr.Failures / 2
					if tr.Calls > 0 {
						tr.SuccessRate = float64(tr.Successes) / float64(tr.Calls)
					}
				}
			}

			for _, sr := range p.StepRecords {
				if sr.Calls > 2 {
					sr.Calls = sr.Calls / 2
					sr.Successes = sr.Successes / 2
					sr.Failures = sr.Failures / 2
					if sr.Calls > 0 {
						sr.SuccessRate = float64(sr.Successes) / float64(sr.Calls)
					}
				}
			}

			decayed++
		}
	}
	return decayed
}

func (r *Registry) Save(path string) error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	data, err := json.MarshalIndent(r.profiles, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal registry: %w", err)
	}

	return os.WriteFile(path, data, 0644)
}

// Load reads the registry from a JSON file.
func (r *Registry) Load(path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no file yet, start fresh
		}
		return fmt.Errorf("read registry: %w", err)
	}

	profiles := make(map[string]*ModelProfile)
	if err := json.Unmarshal(data, &profiles); err != nil {
		return fmt.Errorf("unmarshal registry: %w", err)
	}

	r.profiles = profiles
	return nil
}

func clamp(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
func (r *Registry) minModelCost() float64 {
	min := 0.0
	first := true
	for _, p := range r.profiles {
		if p.AvgCost > 0 {
			if first || p.AvgCost < min {
				min = p.AvgCost
				first = false
			}
		}
	}
	return min
}
