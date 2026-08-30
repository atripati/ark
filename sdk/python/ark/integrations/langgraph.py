"""LangGraph / LangChain integration for ARK — a thin adapter over `ark.trace()`.

    LangGraph lifecycle/events  ->  ARK generic trace/check/record  ->  Go ARK telemetry + supervision

It maps LangChain's callback events onto the generic session and (optionally) gates proposed
tool calls. It reimplements NOTHING — no routing, supervision, retries, pricing, or telemetry;
every field is either reported straight from a LangChain event or derived by Go ARK. What the
adapter can observe automatically vs. what the developer must supply:

  observed automatically by the adapter (from LangChain callbacks)
    - model calls (start/end), model name, input/output tokens  -> ARK derives cost
    - tool calls: name, args, outcome/error, latency
  the developer must still supply (ARK cannot infer it from the graph)
    - which tool to supervise, the applicable constraint, and the trusted runtime evidence
      (e.g. the retrieved, priced options) passed to `ark_supervise_tool(...)`

Interception audit (LangGraph 1.x / langchain-core 1.x): LangChain callback handlers are
OBSERVATIONAL — `on_tool_start` cannot cleanly veto an execution. The clean, framework-native
pre-execution gate is the tool itself: a tool is a plain callable, so wrapping it lets ARK
check the proposed action BEFORE the real logic runs and, on a non-ALLOW verdict, return the
runtime-derived suggestion as the tool result — which LangGraph's own agent loop feeds back to
the model to REPLAN. `ark_supervise_tool` uses exactly that; it fakes no interception.
(`create_react_agent`'s `post_model_hook` / `interrupt_before=["tools"]` are alternative
native gates; the tool wrapper is the thinnest and works with any graph.)

Sync graphs only (the common `.invoke(...)` path); async/parallel tool fan-out is out of scope.
"""
from __future__ import annotations

import time
from typing import Any, Callable, Dict, List, Optional, Tuple

try:
    from langchain_core.callbacks import BaseCallbackHandler
    from langchain_core.tools import BaseTool, StructuredTool
except ModuleNotFoundError as e:  # optional dependency — give a clear, actionable message
    raise ImportError(
        "ARK's LangGraph integration requires the optional 'langgraph' extra. "
        "Install it with:  pip install 'ark-agent-runtime[langgraph]'  "
        "(or:  pip install langgraph langchain-core ). "
        "The core ARK SDK (import ark) does not need it."
    ) from e

from ..session import RunSession, Verdict


class ArkCallbackHandler(BaseCallbackHandler):
    """A LangChain callback handler that reports every model/tool event to an ARK trace.

        with ark.trace("...") as run:
            agent.invoke(inputs, config={"callbacks": [ArkCallbackHandler(run)]})
        result = run.result

    Pass ``record_tools=False`` when tools are gated with :func:`ark_supervise_tool` (the
    wrapper records those tool decisions itself, so the handler should record only the model
    turns to avoid double-counting).
    """

    # this handler only reports; it never blocks or mutates the run.
    raise_error = False

    def __init__(self, run: RunSession, *, record_tools: bool = True):
        self._run = run
        self._record_tools = record_tools
        self._llm_start: Dict[Any, float] = {}
        self._llm_model: Dict[Any, Optional[str]] = {}
        self._tool_start: Dict[Any, float] = {}
        self._tool_meta: Dict[Any, Tuple[str, Optional[dict]]] = {}

    # ---- model calls -> cost-bearing decisions ----
    def on_chat_model_start(self, serialized, messages, *, run_id, **kwargs):
        self._begin_llm(run_id, serialized, kwargs)

    def on_llm_start(self, serialized, prompts, *, run_id, **kwargs):
        self._begin_llm(run_id, serialized, kwargs)

    def _begin_llm(self, run_id, serialized, kwargs):
        self._llm_start[run_id] = time.monotonic()
        ip = kwargs.get("invocation_params") or {}
        name = ip.get("model") or ip.get("model_name")
        if not name and serialized:
            name = (serialized.get("kwargs") or {}).get("model_name") or serialized.get("name")
        self._llm_model[run_id] = name

    def on_llm_end(self, response, *, run_id, **kwargs):
        model, in_tok, out_tok, tool_names = _llm_end_fields(response)
        if not model:
            model = self._llm_model.get(run_id)
        latency_ms = _elapsed_ms(self._llm_start.pop(run_id, None))
        self._llm_model.pop(run_id, None)
        # a model turn that proposed tool(s) is a "model_call"; a final turn is "complete".
        # cost is DERIVED by ARK from the reported tokens+model — the adapter computes none.
        self._run.record(
            action="model_call" if tool_names else "complete",
            model=model, input_tokens=in_tok, output_tokens=out_tok,
            latency_ms=latency_ms, outcome=("proposed_tools" if tool_names else "stop"),
        )

    def on_llm_error(self, error, *, run_id, **kwargs):
        latency_ms = _elapsed_ms(self._llm_start.pop(run_id, None))
        self._run.record(action="model_call", model=self._llm_model.pop(run_id, None),
                         latency_ms=latency_ms, outcome=f"error: {type(error).__name__}",
                         error=_err(error), executed=False)

    # ---- tool executions -> tool-activity decisions (no token cost) ----
    def on_tool_start(self, serialized, input_str, *, run_id, inputs=None, **kwargs):
        if not self._record_tools:
            return
        self._tool_start[run_id] = time.monotonic()
        name = (serialized or {}).get("name") or "tool"
        self._tool_meta[run_id] = (name, inputs if isinstance(inputs, dict) else None)

    def on_tool_end(self, output, *, run_id, **kwargs):
        if not self._record_tools:
            return
        name, args = self._tool_meta.pop(run_id, ("tool", None))
        latency_ms = _elapsed_ms(self._tool_start.pop(run_id, None))
        self._run.record(action="tool_call", tool=name, tool_args=args,
                         latency_ms=latency_ms, outcome="success")

    def on_tool_error(self, error, *, run_id, **kwargs):
        if not self._record_tools:
            return
        name, args = self._tool_meta.pop(run_id, ("tool", None))
        latency_ms = _elapsed_ms(self._tool_start.pop(run_id, None))
        self._run.record(action="tool_call", tool=name, tool_args=args, latency_ms=latency_ms,
                         outcome=f"error: {type(error).__name__}", error=_err(error))


def build_agent(model: Any, tools: Any, **kwargs) -> Any:
    """Construct a LangGraph ReAct-style agent using the SUPPORTED constructor for the
    installed version — ``langchain.agents.create_agent`` (LangGraph/LangChain 1.x) when
    available, else ``langgraph.prebuilt.create_react_agent`` (older, deprecation silenced).

    This is only a convenience so callers avoid the deprecation churn; ARK's observation and
    supervision hooks work identically with either constructor (verified). It builds nothing
    ARK-specific — the returned agent is a plain LangGraph graph you invoke yourself.
    """
    try:
        from langchain.agents import create_agent  # LangChain 1.x, the supported path
        return create_agent(model, tools, **kwargs)
    except ModuleNotFoundError:
        import warnings
        from langgraph.prebuilt import create_react_agent
        with warnings.catch_warnings():
            warnings.simplefilter("ignore")  # its own move-to-langchain deprecation notice
            return create_react_agent(model, tools, **kwargs)


def ark_supervise_tool(run: RunSession, tool: Any, *, constraint: str,
                       evidence: "dict | Callable[[dict], dict]",
                       proposed: "Optional[Callable[[dict], dict]]" = None) -> StructuredTool:
    """Wrap a LangGraph/LangChain tool so ARK gates each proposed call before it executes.

    ``constraint`` names the runtime constraint (e.g. "rank"); ``evidence`` is the trusted
    runtime evidence — a static dict or a ``fn(tool_args) -> dict`` computed from the call.
    ``proposed`` maps the tool args to the ProposedAction dict (default: ``{"option": <first
    arg>}``). On ALLOW the real tool runs and the executed telemetry is recorded on the SAME
    decision as the verdict (``of=verdict``); on any non-ALLOW verdict the tool does NOT run
    and ARK's suggestion is returned as the tool result, so LangGraph's loop replans.

    The wrapper calls only ``run.check`` / ``run.record`` — the verdict, retry budget, and
    recovery all come from Go ARK. Requires the trace to be opened with supervision enabled.
    """
    base = tool if isinstance(tool, BaseTool) else StructuredTool.from_function(tool)
    raw: Callable = base.func if getattr(base, "func", None) is not None else base.invoke
    name, description, args_schema = base.name, base.description, base.args_schema

    def supervised(**kwargs):
        ev = evidence(kwargs) if callable(evidence) else evidence
        prop = proposed(kwargs) if callable(proposed) else _default_proposed(kwargs)
        verdict: Verdict = run.check(proposed_action=prop, constraint=constraint,
                                     evidence=ev or {}, action="tool_call", tool=name)
        if verdict.allowed:
            try:
                result = raw(**kwargs)
            except Exception as exc:  # a supervised action that was allowed but then failed
                run.record(action="tool_call", tool=name, tool_args=kwargs,
                           outcome=f"error: {type(exc).__name__}", error=_err(exc),
                           executed=True, of=verdict)
                raise  # let LangGraph handle the tool error natively
            run.record(action="tool_call", tool=name, tool_args=kwargs,
                       outcome="success", of=verdict)
            return result
        run.record(action="tool_call", tool=name, tool_args=kwargs,
                   outcome=f"rejected:{verdict.verdict}", executed=False, of=verdict)
        # returned to LangGraph as the ToolMessage -> the model re-proposes (replan).
        hint = f" Suggested: {verdict.suggested}." if verdict.suggested else ""
        return f"ARK blocked this action ({verdict.verdict}): {verdict.reason or 'constraint not satisfied'}.{hint}"

    return StructuredTool.from_function(func=supervised, name=name,
                                        description=description, args_schema=args_schema)


# ---- helpers (pure mapping over LangChain event shapes; no ARK logic) ----

import re as _re

# scrub obvious secret shapes from an error string so a provider error can never leak a key.
_SECRET_RE = _re.compile(
    r"(sk-[A-Za-z0-9_\-]{6,}|ghp_[A-Za-z0-9]{6,}|Bearer\s+[A-Za-z0-9._\-]{6,}|"
    r"api[_-]?key\s*[=:]\s*\S+)", _re.IGNORECASE)


def _err(exc: BaseException, limit: int = 300) -> str:
    """A compact, secret-scrubbed error string for the canonical DecisionRecord.error field."""
    msg = _SECRET_RE.sub("[redacted]", str(exc))[:limit]
    return f"{type(exc).__name__}: {msg}" if msg else type(exc).__name__


def _default_proposed(args: dict) -> dict:
    for v in args.values():
        return {"option": v}
    return {"fields": args}


def _elapsed_ms(start: Optional[float]) -> int:
    return int((time.monotonic() - start) * 1000) if start else 0


def _llm_end_fields(response) -> Tuple[Optional[str], int, int, List[str]]:
    """Extract (model, input_tokens, output_tokens, proposed_tool_names) from an LLMResult,
    tolerating both the modern usage_metadata path and the legacy llm_output path."""
    model: Optional[str] = None
    in_tok = out_tok = 0
    tool_names: List[str] = []
    gens = getattr(response, "generations", None) or []
    msg = getattr(gens[0][0], "message", None) if gens and gens[0] else None
    if msg is not None:
        um = getattr(msg, "usage_metadata", None) or {}
        in_tok = int(um.get("input_tokens") or 0)
        out_tok = int(um.get("output_tokens") or 0)
        rm = getattr(msg, "response_metadata", None) or {}
        model = rm.get("model_name") or rm.get("model")
        tool_names = [tc.get("name") for tc in (getattr(msg, "tool_calls", None) or []) if tc.get("name")]
    lo = getattr(response, "llm_output", None) or {}
    if not model:
        model = lo.get("model_name") or lo.get("model")
    if in_tok == 0 and out_tok == 0:
        tu = lo.get("token_usage") or lo.get("usage") or {}
        in_tok = int(tu.get("prompt_tokens") or tu.get("input_tokens") or 0)
        out_tok = int(tu.get("completion_tokens") or tu.get("output_tokens") or 0)
    return model, in_tok, out_tok, tool_names
