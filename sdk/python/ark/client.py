"""The ARK client — the front door to the ARK runtime.

    from ark import ARK
    ark = ARK()
    result = ark.run(task="find the top Python web frameworks on GitHub")
    print(result.success, result.total_cost, result.decisions)

Experimental constrained supervision is explicit and off by default:

    ark = ARK(supervision="experimental")

The Go runtime is the source of truth; this client only submits requests and parses the
canonical RunResult. It reimplements no routing/supervision/cost/verification/retry logic.
"""
from __future__ import annotations

from typing import Any, Optional

from .bridge import SubprocessBridge
from .errors import ArkSupervisionDisabled
from .models import RunResult, SupervisionRecord, SupervisionResult
from .session import RunSession


class ARK:
    def __init__(self, supervision: str = "off", bridge=None, providers: dict = None):
        if supervision not in ("off", "experimental"):
            raise ValueError("supervision must be 'off' or 'experimental'")
        self.supervision = supervision
        self._bridge = bridge or SubprocessBridge()
        # trusted evidence providers, configured by the application — never by the agent.
        self._providers: dict = dict(providers or {})

    def run(self, task: Optional[str] = None, agent: Any = None, *,
            mode: str = "mock", config: Optional[str] = None) -> RunResult:
        """Run a task through the Go ARK runtime and return the canonical RunResult.

        mode="mock" (default) runs the real runtime deterministically with no API cost;
        mode="live" uses the provider configured in agent.yaml (needs a real key).
        """
        if not task and agent is None:
            raise ValueError("run() requires a task")
        req: dict = {"kind": "run", "task": task or "", "mode": mode, "supervision": self.supervision}
        if isinstance(agent, str):  # agent given as a config path
            req["config"] = agent
        if config:
            req["config"] = config
        return RunResult.from_dict(self._bridge.call(req))

    def bridge_info(self) -> dict:
        """Probe the bridge's compatibility contract: {protocol_version, capabilities, bridge}.
        Used to verify a freshly installed wheel bundles a hardened, matching bridge."""
        return self._bridge.call({"kind": "hello"})

    def trace(self, task: str, *, task_type: Optional[str] = None,
              provider: str = "openai", budget: int = 4) -> RunSession:
        """Open an external-agent trace: keep your own agent/model/tools and attach ARK
        around your runtime. Use as a context manager:

            with ark.trace("book the 2nd-cheapest flight") as run:
                ...                       # your loop
                run.record(action="tool_call", model="gpt-4o", input_tokens=..., ...)
            result = run.result           # canonical RunResult

        Supervision follows this client's setting: ARK(supervision="experimental").trace(...)
        enables run.check(...). ARK never executes your agent — you do.
        """
        return RunSession(task, task_type=task_type, supervision=self.supervision,
                          provider=provider, budget=budget, providers=self._providers)

    def supervise(self, constraint: str, proposed: dict, evidence: dict,
                  retry_count: int = 0, budget: int = 4) -> SupervisionResult:
        """Evaluate one agent-authored action against a runtime-derived constraint.

        Experimental: requires ARK(supervision="experimental"). Returns a verdict
        (ALLOW/REJECT/REQUIRE_EVIDENCE/RECOVERY_EXHAUSTED); ARK never authors the action.
        """
        if self.supervision != "experimental":
            raise ArkSupervisionDisabled(
                "constrained supervision is experimental and off by default; "
                "create ARK(supervision='experimental')")
        data = self._bridge.call({
            "kind": "supervise", "supervision": "experimental", "constraint": constraint,
            "proposed": proposed, "evidence": evidence, "retry_count": retry_count, "budget": budget,
        })
        return SupervisionResult(
            verdict=data.get("verdict"), reason=data.get("reason"),
            record=SupervisionRecord.from_dict(data.get("supervision")),
        )
