"""Typed Python mirror of the canonical ARK telemetry contract (pkg/telemetry).

These objects mirror the Go `RunResult` / `DecisionRecord` JSON exactly — they do not
invent a new schema, and they never fabricate a value the runtime reported as absent
(missing fields stay ``None``). No routing/supervision/cost/verification logic lives here;
this is a passive view over what the Go runtime produced.
"""
from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Optional


def _f(d: dict, k: str, default=None):
    return d.get(k, default)


@dataclass(frozen=True)
class DecisionCost:
    input_tokens: int = 0
    output_tokens: int = 0
    input_cost: float = 0.0
    output_cost: float = 0.0
    model_cost: float = 0.0
    tool_cost: float = 0.0
    total_cost: float = 0.0

    @classmethod
    def from_dict(cls, d: Optional[dict]) -> "DecisionCost":
        d = d or {}
        return cls(
            input_tokens=_f(d, "input_tokens", 0),
            output_tokens=_f(d, "output_tokens", 0),
            input_cost=_f(d, "input_cost", 0.0),
            output_cost=_f(d, "output_cost", 0.0),
            model_cost=_f(d, "model_cost", 0.0),
            tool_cost=_f(d, "tool_cost", 0.0),
            total_cost=_f(d, "total_cost", 0.0),
        )


@dataclass(frozen=True)
class Verification:
    method: Optional[str] = None
    passed: Optional[bool] = None
    score: Optional[float] = None
    confidence: Optional[float] = None
    issues: Optional[list] = None

    @classmethod
    def from_dict(cls, d: Optional[dict]) -> Optional["Verification"]:
        if not d:
            return None
        return cls(_f(d, "method"), _f(d, "passed"), _f(d, "score"), _f(d, "confidence"), _f(d, "issues"))


@dataclass(frozen=True)
class SupervisionRecord:
    applicable_constraint: Optional[str] = None
    verdict: Optional[str] = None  # ALLOW / REJECT / REQUIRE_EVIDENCE / RECOVERY_EXHAUSTED
    trusted_evidence_ref: Optional[str] = None
    rejection_reason: Optional[str] = None
    retry_number: Optional[int] = None
    suggested_from_evidence: Optional[str] = None
    executed: Optional[bool] = None

    @classmethod
    def from_dict(cls, d: Optional[dict]) -> Optional["SupervisionRecord"]:
        if not d:
            return None
        return cls(
            _f(d, "applicable_constraint"), _f(d, "verdict"), _f(d, "trusted_evidence_ref"),
            _f(d, "rejection_reason"), _f(d, "retry_number"), _f(d, "suggested_from_evidence"),
            _f(d, "executed"),
        )


@dataclass(frozen=True)
class RoutingDecision:
    """A view of a decision's routing: which model, and why."""
    model: Optional[str] = None
    routing_reason: Optional[str] = None


@dataclass(frozen=True)
class ToolDecision:
    """A view of a decision's tool activity (args are a redacted reference, never secrets)."""
    tool: Optional[str] = None
    tool_args_ref: Optional[str] = None


@dataclass(frozen=True)
class DecisionRecord:
    id: str
    sequence: int
    decision_type: str
    timestamp: Optional[str] = None
    action: Optional[str] = None
    model: Optional[str] = None
    routing_reason: Optional[str] = None
    tool: Optional[str] = None
    tool_args_ref: Optional[str] = None
    cost: DecisionCost = field(default_factory=DecisionCost)
    latency_ms: int = 0
    verification: Optional[Verification] = None
    supervision: Optional[SupervisionRecord] = None
    outcome: Optional[str] = None
    error: Optional[str] = None
    executed: bool = False

    @property
    def latency(self) -> int:
        return self.latency_ms

    @property
    def routing(self) -> RoutingDecision:
        return RoutingDecision(self.model, self.routing_reason)

    @property
    def tool_decision(self) -> ToolDecision:
        return ToolDecision(self.tool, self.tool_args_ref)

    @classmethod
    def from_dict(cls, d: dict) -> "DecisionRecord":
        return cls(
            id=d["id"], sequence=d["sequence"], decision_type=d["decision_type"],
            timestamp=_f(d, "timestamp"), action=_f(d, "action"), model=_f(d, "model"),
            routing_reason=_f(d, "routing_reason"), tool=_f(d, "tool"), tool_args_ref=_f(d, "tool_args_ref"),
            cost=DecisionCost.from_dict(_f(d, "cost")), latency_ms=_f(d, "latency_ms", 0),
            verification=Verification.from_dict(_f(d, "verification")),
            supervision=SupervisionRecord.from_dict(_f(d, "supervision")),
            outcome=_f(d, "outcome"), error=_f(d, "error"), executed=_f(d, "executed", False),
        )


@dataclass(frozen=True)
class SupervisionSummary:
    enabled: bool = False
    interventions: int = 0
    by_verdict: dict = field(default_factory=dict)

    @classmethod
    def from_dict(cls, d: Optional[dict]) -> "SupervisionSummary":
        d = d or {}
        return cls(_f(d, "enabled", False), _f(d, "interventions", 0), _f(d, "by_verdict") or {})


@dataclass(frozen=True)
class RoutingSummary:
    by_model: dict = field(default_factory=dict)

    @classmethod
    def from_dict(cls, d: Optional[dict]) -> "RoutingSummary":
        return cls((d or {}).get("by_model") or {})


@dataclass(frozen=True)
class ToolSummary:
    calls: int = 0
    by_tool: dict = field(default_factory=dict)

    @classmethod
    def from_dict(cls, d: Optional[dict]) -> "ToolSummary":
        d = d or {}
        return cls(_f(d, "calls", 0), _f(d, "by_tool") or {})


@dataclass(frozen=True)
class RunResult:
    run_id: str
    task: str
    success: bool
    decisions: list  # list[DecisionRecord]
    task_type: Optional[str] = None
    started_at: Optional[str] = None
    completed_at: Optional[str] = None
    termination_reason: Optional[str] = None
    output: Optional[str] = None
    total_tokens: int = 0
    total_latency_ms: int = 0
    total_cost: float = 0.0
    cost_by_model: dict = field(default_factory=dict)
    cost_by_tool: dict = field(default_factory=dict)
    cost_by_action: dict = field(default_factory=dict)
    cost_by_supervision: Optional[dict] = None
    supervision: SupervisionSummary = field(default_factory=SupervisionSummary)
    routing: RoutingSummary = field(default_factory=RoutingSummary)
    tools: ToolSummary = field(default_factory=ToolSummary)
    providers: dict = field(default_factory=dict)
    errors: Optional[list] = None
    raw: Optional[dict] = None  # the exact JSON the runtime returned

    @property
    def total_latency(self) -> int:
        return self.total_latency_ms

    @classmethod
    def from_dict(cls, d: dict) -> "RunResult":
        return cls(
            run_id=d["run_id"], task=_f(d, "task", ""), success=_f(d, "success", False),
            decisions=[DecisionRecord.from_dict(x) for x in (_f(d, "decisions") or [])],
            task_type=_f(d, "task_type"), started_at=_f(d, "started_at"), completed_at=_f(d, "completed_at"),
            termination_reason=_f(d, "termination_reason"), output=_f(d, "output"),
            total_tokens=_f(d, "total_tokens", 0), total_latency_ms=_f(d, "total_latency_ms", 0),
            total_cost=_f(d, "total_cost", 0.0),
            cost_by_model=_f(d, "cost_by_model") or {}, cost_by_tool=_f(d, "cost_by_tool") or {},
            cost_by_action=_f(d, "cost_by_action") or {}, cost_by_supervision=_f(d, "cost_by_supervision"),
            supervision=SupervisionSummary.from_dict(_f(d, "supervision")),
            routing=RoutingSummary.from_dict(_f(d, "routing")),
            tools=ToolSummary.from_dict(_f(d, "tools")),
            providers=_f(d, "providers") or {}, errors=_f(d, "errors"), raw=d,
        )

    def to_dict(self) -> dict:
        """The canonical run as a plain dict — exactly the JSON the runtime produced."""
        return dict(self.raw) if self.raw is not None else {}

    def to_json(self, indent: int = 2) -> str:
        """The canonical run as a JSON string."""
        import json
        return json.dumps(self.to_dict(), indent=indent, default=str)

    def report(self, verbose: bool = False, file=None) -> None:
        """Print the human-readable ARK report for this run. Same formatter as
        RunSession.report; per-decision provenance is available via RunSession.report."""
        import sys
        from .report import format_run
        print(format_run(self, verbose=verbose), file=file or sys.stdout)


@dataclass(frozen=True)
class SupervisionResult:
    """Result of one constrained-supervision evaluation. ARK validates/gates; it never
    authors the replacement action — the agent re-proposes."""
    verdict: str  # ALLOW / REJECT / REQUIRE_EVIDENCE / RECOVERY_EXHAUSTED
    reason: Optional[str] = None
    record: Optional[SupervisionRecord] = None

    @property
    def allowed(self) -> bool:
        return self.verdict == "ALLOW"

    @property
    def suggested_from_evidence(self) -> Optional[str]:
        return self.record.suggested_from_evidence if self.record else None
