package telemetry

import (
	"github.com/atripati/ark/pkg/router"
	"github.com/atripati/ark/pkg/runtime"
)

// FromTaskResult builds the canonical RunResult directly from a live ARK run's result.
// This is the integration seam: given the TaskResult that runtime.Agent.Run returns
// (which already carries the real CostReport) plus the router's recorded decisions, it
// produces one DecisionRecord per actual runtime step and derives all run-level views.
//
// It is read-only observability: it changes no runtime behavior, and it never serializes
// secrets (it reads step cost/token/latency/action/tool + model/routing_reason only — never
// prompts, args, or credentials).
//
// The single live wire-up is one call at the end of runAgent, e.g.:
//
//	rr := telemetry.FromTaskResult(task, &result, modelRouter.Decisions())
//	if jsonOut { b, _ := rr.JSON(); fmt.Println(string(b)) }
//
// (Left unwired here to avoid touching unrelated in-progress work in runAgent.)
func FromTaskResult(task string, tr *runtime.TaskResult, routing []router.Decision) RunResult {
	b := NewRun(tr.TaskID, task)

	routeByStep := make(map[int]router.Decision, len(routing))
	for _, d := range routing {
		routeByStep[d.Step] = d
	}

	// The CostReport steps are the authoritative per-decision list (each priced step that
	// ran). Enrich each with the router decision for that step (model + routing reason).
	fallbackModel := ""
	if tr.CostReport != nil {
		fallbackModel = tr.CostReport.Model
		for _, sc := range tr.CostReport.Steps {
			d := DecisionRecord{
				DecisionType: mapDecisionType(sc.Action),
				Action:       sc.Action,
				Tool:         sc.ToolName,
				Model:        fallbackModel,
				Cost: Cost{
					InputTokens:  sc.InputTokens,
					OutputTokens: sc.OutputTokens,
					InputCost:    sc.InputCost,
					OutputCost:   sc.OutputCost,
					ModelCost:    sc.TotalCost, // ARK attributes all step cost to the model
					TotalCost:    sc.TotalCost,
				},
				LatencyMs: sc.Latency.Milliseconds(),
				Executed:  true,
			}
			if rd, ok := routeByStep[sc.Step]; ok {
				if rd.ModelUsed != "" {
					d.Model = rd.ModelUsed
				}
				d.RoutingReason = rd.Reason
			}
			b.AddDecision(d)
		}
	}

	termination := "complete"
	if !tr.Success {
		termination = "error"
	}
	return b.Finish(tr.Success, termination, tr.Output)
}

func mapDecisionType(action string) DecisionType {
	switch action {
	case "tool_call":
		return DecisionToolCall
	case "complete":
		return DecisionComplete
	case "retry":
		return DecisionRetry
	case "grounding", "grounding_rejected":
		return DecisionGrounding
	default:
		return DecisionType(action)
	}
}
