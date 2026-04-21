package governor

import (
	"strings"
)

type TaskType string

const (
	TaskRetrieval     TaskType = "retrieval"
	TaskReasoning     TaskType = "reasoning"
	TaskSummarization TaskType = "summarization"
	TaskMultiStep     TaskType = "multi_step"
	TaskCoding        TaskType = "coding"
	TaskGeneral       TaskType = "general"
)

func ClassifyTask(query string) TaskType {
	q := strings.ToLower(query)

	multiStepSignals := []string{
		"and then", "after that", "step by step", "first", "finally",
		"compare", "analyze and", "find and", "list and",
	}
	multiCount := 0
	for _, sig := range multiStepSignals {
		if strings.Contains(q, sig) {
			multiCount++
		}
	}
	if multiCount >= 2 || strings.Count(q, ",") >= 3 {
		return TaskMultiStep
	}

	// Coding
	codingSignals := []string{
		"code", "function", "implement", "debug", "fix the bug",
		"write a script", "refactor", "class", "algorithm",
		"compile", "syntax", "api endpoint", "unit test",
	}
	for _, sig := range codingSignals {
		if strings.Contains(q, sig) {
			return TaskCoding
		}
	}

	// Summarization
	summarizeSignals := []string{
		"summarize", "summary", "tldr", "brief", "overview",
		"key points", "main ideas", "recap", "condense",
	}
	for _, sig := range summarizeSignals {
		if strings.Contains(q, sig) {
			return TaskSummarization
		}
	}

	// Reasoning
	reasoningSignals := []string{
		"why", "explain", "reason", "analyze", "evaluate",
		"what do you think", "pros and cons", "trade-off",
		"should i", "which is better", "decide", "conclude",
		"implication", "consequence", "cause",
	}
	for _, sig := range reasoningSignals {
		if strings.Contains(q, sig) {
			return TaskReasoning
		}
	}

	// Retrieval
	retrievalSignals := []string{
		"find", "search", "list", "get", "show me", "look up",
		"fetch", "what is", "who is", "how many", "check",
		"status", "latest", "current",
	}
	for _, sig := range retrievalSignals {
		if strings.Contains(q, sig) {
			return TaskRetrieval
		}
	}

	return TaskGeneral
}

// FailurePrediction is the Teacher's instinct about whether a model will fail.
type FailurePrediction struct {
	ShouldAvoid bool    `json:"should_avoid"`
	Risk        float64 `json:"risk"` // 0.0 = safe, 1.0 = will definitely fail
	Reason      string  `json:"reason"`
	Alternative string  `json:"alternative"` // suggested model instead
}

func (r *Registry) PredictFailure(model string, taskType TaskType, toolName string) FailurePrediction {
	r.mu.RLock()
	defer r.mu.RUnlock()

	p, ok := r.profiles[model]
	if !ok {
		return FailurePrediction{ShouldAvoid: false, Risk: 0.0, Reason: "no history"}
	}

	risk := 0.0
	reasons := make([]string, 0)

	// Signal 1: Model has failed 3+ times on this specific tool
	if toolName != "" {
		if tr, ok := p.ToolRecords[toolName]; ok {
			if tr.Calls >= 3 && tr.SuccessRate < 0.4 {
				risk += 0.5
				reasons = append(reasons, "low tool success rate")
			}
		}
	}

	// Signal 2: Model has failed on this task type
	taskKey := string(taskType)
	if sr, ok := p.StepRecords[taskKey]; ok {
		if sr.Calls >= 3 && sr.SuccessRate < 0.5 {
			risk += 0.3
			reasons = append(reasons, "low task-type success rate")
		}
	}

	// Signal 3: High hallucination rate
	if p.HallucinationRate > 0.2 {
		risk += 0.2
		reasons = append(reasons, "high hallucination rate")
	}

	// Signal 4: Recent failures (last failure patterns)
	if toolName != "" {
		if tr, ok := p.ToolRecords[toolName]; ok {
			if tr.Failures >= 3 && tr.LastFailure.After(tr.LastSuccess) {
				risk += 0.2
				reasons = append(reasons, "recent consecutive failures")
			}
		}
	}

	risk = clamp(risk)
	shouldAvoid := risk >= 0.5

	reason := "no risk detected"
	if len(reasons) > 0 {
		reason = strings.Join(reasons, "; ")
	}

	// Find alternative
	alternative := ""
	if shouldAvoid {
		alternative = r.findAlternative(model, taskKey, toolName)
	}

	return FailurePrediction{
		ShouldAvoid: shouldAvoid,
		Risk:        risk,
		Reason:      reason,
		Alternative: alternative,
	}
}

// findAlternative finds the best alternative model. Must be called with r.mu held.
func (r *Registry) findAlternative(excludeModel, stepType, toolName string) string {
	bestModel := ""
	bestScore := -1.0

	for name, _ := range r.profiles {
		if name == excludeModel {
			continue
		}
		score := r.scoreModelFor(name, stepType, toolName)
		if score > bestScore {
			bestScore = score
			bestModel = name
		}
	}
	return bestModel
}

type EffortLevel string

const (
	EffortLow    EffortLevel = "low"    // cheap model only, no retries
	EffortMedium EffortLevel = "medium" // cheap model + 1 retry allowed
	EffortHigh   EffortLevel = "high"   // strong model + verification + retry
)

// EffortPlan describes the compute budget for a task.
type EffortPlan struct {
	Level         EffortLevel `json:"level"`
	PreferredTier string      `json:"preferred_tier"` // "fast" or "strong"
	MaxRetries    int         `json:"max_retries"`
	VerifyOutput  bool        `json:"verify_output"`
	AllowDisagree bool        `json:"allow_disagree"` // run second model to compare
	Reason        string      `json:"reason"`
}

// model history, and the last verification confidence.
func DetermineEffort(taskType TaskType, lastConfidence float64, prediction FailurePrediction) EffortPlan {
	// High risk or complex task → high effort
	if prediction.Risk >= 0.5 || taskType == TaskMultiStep {
		return EffortPlan{
			Level:         EffortHigh,
			PreferredTier: "strong",
			MaxRetries:    2,
			VerifyOutput:  true,
			AllowDisagree: true,
			Reason:        "high risk or complex task",
		}
	}

	// Low confidence from previous step → medium effort
	if lastConfidence > 0 && lastConfidence < 0.6 {
		return EffortPlan{
			Level:         EffortMedium,
			PreferredTier: "strong",
			MaxRetries:    1,
			VerifyOutput:  true,
			AllowDisagree: false,
			Reason:        "low confidence from previous step",
		}
	}

	// Reasoning/coding tasks → medium effort
	if taskType == TaskReasoning || taskType == TaskCoding {
		return EffortPlan{
			Level:         EffortMedium,
			PreferredTier: "strong",
			MaxRetries:    1,
			VerifyOutput:  true,
			AllowDisagree: false,
			Reason:        "reasoning/coding benefits from strong model",
		}
	}

	// Simple retrieval/summarization → low effort
	return EffortPlan{
		Level:         EffortLow,
		PreferredTier: "fast",
		MaxRetries:    0,
		VerifyOutput:  false,
		AllowDisagree: false,
		Reason:        "simple task, cheap model sufficient",
	}
}

// BuildExperienceContext generates a prompt injection that tells the model
// about past failures. This is the Teacher sharing experience with the student.
func (r *Registry) BuildExperienceContext(model string, toolName string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	p, ok := r.profiles[model]
	if !ok {
		return ""
	}

	var hints []string

	// Warn about tool-specific failures
	if toolName != "" {
		if tr, ok := p.ToolRecords[toolName]; ok {
			if tr.Failures > 0 && tr.SuccessRate < 0.7 {
				hints = append(hints, "Previous attempts with this tool have had failures. Double-check your parameters and output format.")
			}
		}
	}

	// Warn about hallucination tendency
	if p.HallucinationRate > 0.1 {
		hints = append(hints, "You have a tendency to answer without using tools. Always call a tool first when tools are available.")
	}

	// Warn about specific failure patterns
	if count, ok := p.FailurePatterns["invalid_json"]; ok && count >= 2 {
		hints = append(hints, "Previous responses had invalid JSON. Ensure your output is valid JSON.")
	}
	if count, ok := p.FailurePatterns["empty_output"]; ok && count >= 2 {
		hints = append(hints, "Previous responses were empty. Provide a complete response.")
	}

	if len(hints) == 0 {
		return ""
	}

	return "\n[ARK Governor Notes: " + strings.Join(hints, " ") + "]\n"
}

// DisagreementResult holds the outcome of comparing two model outputs.
type DisagreementResult struct {
	Agree      bool    `json:"agree"`
	Similarity float64 `json:"similarity"` // 0.0 = completely different, 1.0 = identical
	Reason     string  `json:"reason"`
}

// CheckDisagreement compares two model outputs to detect if they fundamentally disagree.
// Simple version: Jaccard similarity on word sets.
func CheckDisagreement(output1, output2 string) DisagreementResult {
	if output1 == "" || output2 == "" {
		return DisagreementResult{Agree: false, Similarity: 0, Reason: "one or both outputs empty"}
	}

	words1 := wordSet(output1)
	words2 := wordSet(output2)

	// Jaccard similarity
	intersection := 0
	for w := range words1 {
		if _, ok := words2[w]; ok {
			intersection++
		}
	}

	union := len(words1) + len(words2) - intersection
	if union == 0 {
		return DisagreementResult{Agree: true, Similarity: 1.0, Reason: "both empty"}
	}

	similarity := float64(intersection) / float64(union)

	agree := similarity > 0.3 // threshold: 30% word overlap = rough agreement
	reason := "outputs agree"
	if !agree {
		reason = "outputs significantly disagree — consider using strong model"
	}

	return DisagreementResult{
		Agree:      agree,
		Similarity: similarity,
		Reason:     reason,
	}
}

func wordSet(s string) map[string]struct{} {
	words := strings.Fields(strings.ToLower(s))
	set := make(map[string]struct{}, len(words))
	for _, w := range words {
		// Strip punctuation
		w = strings.Trim(w, ".,;:!?\"'()[]{}—-")
		if len(w) > 2 { // skip short words
			set[w] = struct{}{}
		}
	}
	return set
}
