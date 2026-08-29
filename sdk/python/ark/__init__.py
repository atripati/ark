"""ARK Runtime SDK — a thin Python client over the Go ARK runtime and its canonical
telemetry contract (RunResult / DecisionRecord). Execution, decision traces, cost
attribution, routing info, tool activity, verification (where available), audit/
intervention records, and (experimental, opt-in) constrained supervision.

This SDK does not reimplement ARK and makes no unproven claims (e.g. automatic cost
savings): it exposes the factual telemetry the Go runtime already produces.
"""
from .client import ARK
from .errors import ArkError, ArkBridgeError, ArkSupervisionDisabled
from .models import (
    RunResult, DecisionRecord, DecisionCost, SupervisionRecord, SupervisionResult,
    Verification, RoutingDecision, ToolDecision, SupervisionSummary, RoutingSummary, ToolSummary,
)

__all__ = [
    "ARK", "ArkError", "ArkBridgeError", "ArkSupervisionDisabled",
    "RunResult", "DecisionRecord", "DecisionCost", "SupervisionRecord", "SupervisionResult",
    "Verification", "RoutingDecision", "ToolDecision",
    "SupervisionSummary", "RoutingSummary", "ToolSummary",
]
__version__ = "0.1.0"
