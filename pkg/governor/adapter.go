package governor

import (
	"github.com/atripati/ark/pkg/runtime"
)

type RegistryAdapter struct {
	R *Registry
}

func (ra *RegistryAdapter) Record(model string, kind string, stepType string, toolName string, latencyMs float64, cost float64, errorClass string) {
	obs := Observation{
		Model:      model,
		Kind:       ObservationKind(kind),
		StepType:   stepType,
		ToolName:   toolName,
		LatencyMs:  latencyMs,
		Cost:       cost,
		ErrorClass: errorClass,
	}
	ra.R.Record(obs)
}

func (ra *RegistryAdapter) ShouldDemote(model string, toolName string) bool {
	return ra.R.ShouldDemote(model, toolName)
}

func (ra *RegistryAdapter) FormatReport() string {
	return ra.R.FormatReport()
}

func (ra *RegistryAdapter) PredictFailure(model string, taskType string, toolName string) (bool, float64, string) {
	pred := ra.R.PredictFailure(model, TaskType(taskType), toolName)
	return pred.ShouldAvoid, pred.Risk, pred.Reason
}

func (ra *RegistryAdapter) BuildExperienceContext(model string, toolName string) string {
	return ra.R.BuildExperienceContext(model, toolName)
}

// VerifierAdapter wraps Verifier to implement runtime.GovernorVerifier.
type VerifierAdapter struct {
	V *Verifier
}

func (va *VerifierAdapter) VerifyToolCall(model string, toolName string, call *runtime.ToolCall, response *runtime.ModelResponse) runtime.GovernorVerdict {
	result := va.V.VerifyToolCall(model, toolName, call, response)
	return runtime.GovernorVerdict{
		Passed:     result.Passed,
		Reason:     result.Reason,
		Confidence: result.Confidence,
		Flags:      result.Flags,
	}
}

func (va *VerifierAdapter) VerifyReasoning(model string, response *runtime.ModelResponse, toolsAvailable bool, toolsCalled bool) runtime.GovernorVerdict {
	result := va.V.VerifyReasoning(model, response, toolsAvailable, toolsCalled)
	return runtime.GovernorVerdict{
		Passed:     result.Passed,
		Reason:     result.Reason,
		Confidence: result.Confidence,
		Flags:      result.Flags,
	}
}

func (va *VerifierAdapter) ShouldEscalate(taskID string, passed bool) bool {
	result := VerifyResult{Verdict: VerdictFail}
	if passed {
		result.Verdict = VerdictPass
	}
	return va.V.ShouldEscalate(taskID, result)
}

func (va *VerifierAdapter) ResetEscalations(taskID string) {
	va.V.ResetEscalations(taskID)
}

// NewGovernorForRuntime creates a Governor that can be passed to runtime.NewAgentWithGovernor.
func NewGovernorForRuntime(registry *Registry, verifier *Verifier) *runtime.Governor {
	return &runtime.Governor{
		Registry: &RegistryAdapter{R: registry},
		Verifier: &VerifierAdapter{V: verifier},
	}
}
