package governor

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/atripati/ark/pkg/runtime"
)

// VerifyResult is the Teacher's verdict on a student's answer.
type VerifyResult struct {
	Passed     bool          `json:"passed"`
	Verdict    Verdict       `json:"verdict"`
	Reason     string        `json:"reason"`
	Confidence float64       `json:"confidence"` // 0.0 to 1.0
	Flags      []string      `json:"flags,omitempty"`
	Duration   time.Duration `json:"duration_ms"`
}

// Verdict describes what the verifier concluded.
type Verdict string

const (
	VerdictPass     Verdict = "pass"
	VerdictSuspect  Verdict = "suspect"
	VerdictFail     Verdict = "fail"
	VerdictEscalate Verdict = "escalate"
)

// VerifierConfig controls how strict the verifier is.
type VerifierConfig struct {
	// Structural checks
	CheckEmptyOutput    bool `json:"check_empty_output"`
	CheckToolCallFormat bool `json:"check_tool_call_format"`
	CheckJSONValidity   bool `json:"check_json_validity"`

	// Behavioral checks
	CheckHallucination bool `json:"check_hallucination"`
	CheckConfabulation bool `json:"check_confabulation"`
	CheckRepetition    bool `json:"check_repetition"`

	// Thresholds
	MinOutputLength     int     `json:"min_output_length"`
	MaxRepetitionRatio  float64 `json:"max_repetition_ratio"`
	ConfidenceThreshold float64 `json:"confidence_threshold"`

	// Escalation
	EscalateOnFail bool `json:"escalate_on_fail"`
	MaxEscalations int  `json:"max_escalations"`
}

// DefaultVerifierConfig returns production-safe defaults.
func DefaultVerifierConfig() VerifierConfig {
	return VerifierConfig{
		CheckEmptyOutput:    true,
		CheckToolCallFormat: true,
		CheckJSONValidity:   true,
		CheckHallucination:  true,
		CheckConfabulation:  true,
		CheckRepetition:     true,

		MinOutputLength:     5,
		MaxRepetitionRatio:  0.6,
		ConfidenceThreshold: 0.4,

		EscalateOnFail: true,
		MaxEscalations: 2,
	}
}

type Verifier struct {
	config   VerifierConfig
	registry *Registry

	escalations map[string]int
}

// NewVerifier creates a verifier linked to the capability registry.
func NewVerifier(config VerifierConfig, registry *Registry) *Verifier {
	return &Verifier{
		config:      config,
		registry:    registry,
		escalations: make(map[string]int),
	}
}

func (v *Verifier) VerifyToolCall(model string, toolName string, call *runtime.ToolCall, response *runtime.ModelResponse) VerifyResult {
	start := time.Now()
	flags := make([]string, 0)
	confidence := 1.0

	// Check 1: Empty or nil response
	if response == nil {
		return VerifyResult{
			Passed:   false,
			Verdict:  VerdictFail,
			Reason:   "model returned nil response",
			Flags:    []string{"nil_response"},
			Duration: time.Since(start),
		}
	}

	// Check 2: Empty output
	if v.config.CheckEmptyOutput {
		text := response.Text
		if call != nil && call.Result != "" {
			text = call.Result
		}
		if len(strings.TrimSpace(text)) < v.config.MinOutputLength {
			flags = append(flags, "empty_output")
			confidence *= 0.5
		}
	}

	// Check 3: Tool call format validation
	if v.config.CheckToolCallFormat && call != nil {
		if call.Name == "" {
			flags = append(flags, "missing_tool_name")
			confidence *= 0.4
		}
		if call.Error != nil {
			flags = append(flags, "tool_error")
			confidence *= 0.6
		}
	}

	// Check 4: JSON validity of tool results
	if v.config.CheckJSONValidity && call != nil && call.Result != "" {
		if looksLikeJSON(call.Result) && !isValidJSON(call.Result) {
			flags = append(flags, "invalid_json_result")
			confidence *= 0.6
		}
	}

	// Check 5: Repetition detection (model stuck in loop)
	if v.config.CheckRepetition && response.Text != "" {
		ratio := repetitionRatio(response.Text)
		if ratio > v.config.MaxRepetitionRatio {
			flags = append(flags, "high_repetition")
			confidence *= 0.5
		}
	}

	// Check 6: Confabulation detection — output contains common hallucination markers
	if v.config.CheckConfabulation && response.Text != "" {
		if hasConfabulationMarkers(response.Text) {
			flags = append(flags, "possible_confabulation")
			confidence *= 0.7
		}
	}

	confidence = clamp(confidence)

	// Base cap from structural verification
	maxConf := 0.82

	if v.registry != nil && model != "" {
		p := v.registry.GetProfile(model)
		if p != nil {
			// Signal 1: Model's overall reliability adjusts the ceiling
			if p.TotalCalls >= 5 {
				// 100% success → +0.03, 80% → +0.02, 50% → 0
				maxConf += p.SuccessRate * 0.03
			}

			// Signal 2: Tool-specific track record
			if toolName != "" {
				if tr, ok := p.ToolRecords[toolName]; ok {
					if tr.Calls >= 5 && tr.SuccessRate >= 0.9 {
						maxConf += 0.02 // proven tool
					} else if tr.Calls >= 3 && tr.SuccessRate < 0.7 {
						maxConf -= 0.05 // shaky tool
					} else if tr.Calls < 3 {
						maxConf -= 0.03 // too few observations
					}
				} else {
					maxConf -= 0.04 // never used this tool before
				}
			}

			// Signal 3: Hallucination history penalizes
			if p.HallucinationRate > 0.05 {
				maxConf -= p.HallucinationRate * 0.10
			}
		}
	}

	// Signal 4: Response quality — longer, structured responses get slight boost
	if call != nil && call.Result != "" {
		resultLen := len(call.Result)
		if resultLen > 500 {
			maxConf += 0.01 // substantial response
		} else if resultLen < 20 {
			maxConf -= 0.03 // suspiciously short
		}
	}

	// Clamp final confidence
	maxConf = clamp(maxConf)
	if maxConf < 0.40 {
		maxConf = 0.40 // floor
	}
	if confidence > maxConf {
		confidence = maxConf
	}
	verdict, passed := v.determineVerdict(confidence, flags)

	result := VerifyResult{
		Passed:     passed,
		Verdict:    verdict,
		Confidence: confidence,
		Flags:      flags,
		Duration:   time.Since(start),
	}

	if !passed {
		result.Reason = fmt.Sprintf("verification failed: %s", strings.Join(flags, ", "))
	} else if len(flags) > 0 {
		result.Reason = fmt.Sprintf("passed with warnings: %s", strings.Join(flags, ", "))
	} else {
		result.Reason = "structural checks passed"
	}

	// Record observation in the registry
	if v.registry != nil {
		obs := Observation{
			Model:    model,
			ToolName: toolName,
			StepType: "tool_call",
		}
		if passed {
			obs.Kind = ObsVerificationPassed
		} else {
			obs.Kind = ObsVerificationFailed
			if len(flags) > 0 {
				obs.ErrorClass = flags[0]
			}
		}
		v.registry.Record(obs)
	}

	return result
}

// VerifyReasoning checks a final reasoning/completion response.
func (v *Verifier) VerifyReasoning(model string, response *runtime.ModelResponse, toolsWereAvailable bool, toolsWereCalled bool) VerifyResult {
	start := time.Now()
	flags := make([]string, 0)
	confidence := 1.0

	if response == nil {
		return VerifyResult{
			Passed:   false,
			Verdict:  VerdictFail,
			Reason:   "model returned nil response",
			Flags:    []string{"nil_response"},
			Duration: time.Since(start),
		}
	}

	// Check 1: Empty output
	if v.config.CheckEmptyOutput && len(strings.TrimSpace(response.Text)) < v.config.MinOutputLength {
		flags = append(flags, "empty_reasoning")
		confidence *= 0.4
	}

	// Check 2: Hallucination — model answered without using tools when tools were available
	if v.config.CheckHallucination && toolsWereAvailable && !toolsWereCalled {
		flags = append(flags, "ungrounded_response")
		confidence *= 0.3

		// Record hallucination in registry
		if v.registry != nil {
			v.registry.Record(Observation{
				Model:      model,
				Kind:       ObsHallucination,
				StepType:   "complete",
				ErrorClass: "ungrounded_response",
			})
		}
	}

	// Check 3: Repetition
	if v.config.CheckRepetition {
		ratio := repetitionRatio(response.Text)
		if ratio > v.config.MaxRepetitionRatio {
			flags = append(flags, "repetitive_reasoning")
			confidence *= 0.6
		}
	}

	// Check 4: Confabulation markers
	if v.config.CheckConfabulation {
		if hasConfabulationMarkers(response.Text) {
			flags = append(flags, "possible_confabulation")
			confidence *= 0.7
		}
	}

	confidence = clamp(confidence)

	// Rich confidence calibration for reasoning
	maxConf := 0.80 // base for grounded reasoning

	if toolsWereAvailable && toolsWereCalled {
		maxConf = 0.80

		if v.registry != nil && model != "" {
			p := v.registry.GetProfile(model)
			if p != nil {
				// Model reliability bonus
				if p.TotalCalls >= 5 && p.SuccessRate >= 0.9 {
					maxConf += 0.04
				} else if p.TotalCalls >= 5 && p.SuccessRate < 0.7 {
					maxConf -= 0.05
				}

				// Hallucination history penalty
				if p.HallucinationRate > 0.05 {
					maxConf -= p.HallucinationRate * 0.15
				}
			}
		}

		// Response quality signals
		textLen := len(strings.TrimSpace(response.Text))
		if textLen > 500 {
			maxConf += 0.02
		} else if textLen > 200 {
			maxConf += 0.01
		} else if textLen < 50 {
			maxConf -= 0.05
		}

		if strings.Contains(response.Text, "**") || strings.Contains(response.Text, "http") {
			maxConf += 0.01 // structured output
		}

	} else if toolsWereAvailable && !toolsWereCalled {
		maxConf = 0.45 // ungrounded — hard cap
	} else {
		maxConf = 0.70 // pure reasoning, no tools
	}

	maxConf = clamp(maxConf)
	if maxConf < 0.40 {
		maxConf = 0.40
	}
	if confidence > maxConf {
		confidence = maxConf
	}
	verdict, passed := v.determineVerdict(confidence, flags)

	result := VerifyResult{
		Passed:     passed,
		Verdict:    verdict,
		Confidence: confidence,
		Flags:      flags,
		Duration:   time.Since(start),
	}

	if !passed {
		result.Reason = fmt.Sprintf("reasoning verification failed: %s", strings.Join(flags, ", "))
	} else if len(flags) > 0 {
		result.Reason = fmt.Sprintf("passed with warnings: %s", strings.Join(flags, ", "))
	} else {
		result.Reason = "reasoning structurally verified"
	}

	// Record in registry
	if v.registry != nil {
		obs := Observation{
			Model:    model,
			StepType: "complete",
		}
		if passed {
			obs.Kind = ObsReasoningSuccess
		} else {
			obs.Kind = ObsReasoningFailure
			if len(flags) > 0 {
				obs.ErrorClass = flags[0]
			}
		}
		v.registry.Record(obs)
	}

	return result
}

// ShouldEscalate checks if a failed verification should trigger escalation to a stronger model.
func (v *Verifier) ShouldEscalate(taskID string, result VerifyResult) bool {
	if !v.config.EscalateOnFail {
		return false
	}

	if result.Verdict != VerdictFail && result.Verdict != VerdictEscalate {
		return false
	}

	count := v.escalations[taskID]
	if count >= v.config.MaxEscalations {
		return false // prevent infinite escalation loops
	}

	v.escalations[taskID]++
	return true
}

func (v *Verifier) ResetEscalations(taskID string) {
	delete(v.escalations, taskID)
}

func (v *Verifier) determineVerdict(confidence float64, flags []string) (Verdict, bool) {
	// Hard fails
	for _, f := range flags {
		switch f {
		case "nil_response", "ungrounded_response":
			return VerdictFail, false
		}
	}

	if confidence < v.config.ConfidenceThreshold {
		return VerdictFail, false
	}

	if confidence < 0.6 {
		return VerdictSuspect, true // pass but warn
	}

	if confidence < 0.8 && len(flags) > 0 {
		return VerdictSuspect, true
	}

	return VerdictPass, true
}

// FormatResult returns a human-readable verification result.
func FormatResult(vr VerifyResult) string {
	icon := "✓"
	switch vr.Verdict {
	case VerdictFail:
		icon = "✗"
	case VerdictSuspect:
		icon = "⚠"
	case VerdictEscalate:
		icon = "↑"
	}

	return fmt.Sprintf("  %s Verify: %s (confidence: %.0f%%) %s [%v]",
		icon, vr.Verdict, vr.Confidence*100, vr.Reason, vr.Duration.Round(time.Microsecond))
}

func looksLikeJSON(s string) bool {
	s = strings.TrimSpace(s)
	return (strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}")) ||
		(strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]"))
}

func isValidJSON(s string) bool {
	var js json.RawMessage
	return json.Unmarshal([]byte(s), &js) == nil
}

// repetitionRatio detects if the model is stuck in a loop by checking for repeated phrases.
func repetitionRatio(text string) float64 {
	if len(text) < 50 {
		return 0
	}

	words := strings.Fields(text)
	if len(words) < 10 {
		return 0
	}

	// Check for repeated 3-word sequences
	trigrams := make(map[string]int)
	for i := 0; i <= len(words)-3; i++ {
		tri := words[i] + " " + words[i+1] + " " + words[i+2]
		trigrams[tri]++
	}

	totalTrigrams := len(words) - 2
	repeatedCount := 0
	for _, count := range trigrams {
		if count > 1 {
			repeatedCount += count - 1
		}
	}

	if totalTrigrams == 0 {
		return 0
	}
	return float64(repeatedCount) / float64(totalTrigrams)
}

// hasConfabulationMarkers checks for common patterns when models make things up.
func hasConfabulationMarkers(text string) bool {
	lower := strings.ToLower(text)
	markers := []string{
		"i don't have access to",
		"i cannot access",
		"as an ai",
		"i don't have real-time",
		"based on my training data",
		"i cannot verify",
		"let me make up",
		"hypothetically",
	}

	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
