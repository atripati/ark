"""ARK Runtime SDK — a thin Python client over the Go ARK runtime and its canonical
telemetry contract (RunResult / DecisionRecord). Execution, decision traces, cost
attribution, routing info, tool activity, verification (where available), audit/
intervention records, and (experimental, opt-in) constrained supervision.

This SDK does not reimplement ARK and makes no unproven claims (e.g. automatic cost
savings): it exposes the factual telemetry the Go runtime already produces.
"""
from typing import Optional

from .client import ARK
from .errors import ArkError, ArkBridgeError, ArkSupervisionDisabled
from .session import RunSession, Verdict
from .report import format_run
from .models import (
    RunResult, DecisionRecord, DecisionCost, SupervisionRecord, SupervisionResult,
    Verification, RoutingDecision, ToolDecision, SupervisionSummary, RoutingSummary, ToolSummary,
)


def trace(task: str, *, supervision: str = "off", task_type: Optional[str] = None,
          provider: str = "openai", budget: int = 4) -> RunSession:
    """Open an external-agent trace with a default client. Keep your own agent/model/tools
    and attach ARK around your runtime:

        import ark
        with ark.trace("find the top Python web frameworks") as run:
            ...                                  # your loop reports decisions
        result = run.result                      # canonical RunResult

    Pass supervision="experimental" to enable run.check(...) gating.
    """
    return ARK(supervision=supervision).trace(task, task_type=task_type,
                                              provider=provider, budget=budget)


__all__ = [
    "ARK", "trace", "RunSession", "Verdict", "format_run",
    "ArkError", "ArkBridgeError", "ArkSupervisionDisabled",
    "RunResult", "DecisionRecord", "DecisionCost", "SupervisionRecord", "SupervisionResult",
    "Verification", "RoutingDecision", "ToolDecision",
    "SupervisionSummary", "RoutingSummary", "ToolSummary",
]
__version__ = "0.1.0a2"
