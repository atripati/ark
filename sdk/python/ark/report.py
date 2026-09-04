"""Human-readable ARK run report, built only from the canonical RunResult.

`format_run(result)` returns the report string; `RunSession.report()` / `RunResult.report()`
print it. It creates no parallel telemetry schema and invents no fields: everything comes
from the RunResult the Go runtime already produced. Missing optional fields are omitted, never
faked. Reported-vs-derived facts stay distinguishable — verbose mode prints the per-decision
provenance. Nothing here can leak secrets: tool args arrive already redacted (tool_args_ref)
and providers are status only, so the report only prints what the canonical result exposes.
"""
from __future__ import annotations

import json
from typing import Any, List, Optional


def _money(x: Any) -> str:
    try:
        x = float(x)
    except (TypeError, ValueError):
        return "-"
    if x == 0:
        return "$0.00"
    return "$" + f"{x:.8f}".rstrip("0").rstrip(".")


def _kv_money(d: Optional[dict]) -> str:
    if not d:
        return "-"
    return ", ".join(f"{k} {_money(v)}" for k, v in sorted(d.items()))


def _kv_int(d: Optional[dict]) -> str:
    if not d:
        return "-"
    return ", ".join(f"{k} {v}" for k, v in sorted(d.items()))


def _short(s: Any, n: int = 200) -> str:
    s = str(s)
    return s if len(s) <= n else s[: n - 1] + "…"


def _tool_call(d) -> str:
    """Render tool + its (already redacted) args as `book(option=C)`, or just the tool name."""
    if not d.tool:
        return ""
    if d.tool_args_ref:
        try:
            parsed = json.loads(d.tool_args_ref)
            if isinstance(parsed, dict) and parsed:
                return f"{d.tool}(" + ", ".join(f"{k}={v}" for k, v in parsed.items()) + ")"
        except (ValueError, TypeError):
            pass
    return d.tool


def _kind(d) -> str:
    if d.tool:
        return "TOOL"
    if d.model:
        return "MODEL"
    if d.supervision:
        return "SUPERVISION"
    return (d.decision_type or "STEP").upper()


def _trace_line(d) -> str:
    cells: List[str] = [d.id, _kind(d).ljust(5)]
    if d.tool:
        cells.append(_tool_call(d))
    if d.model:
        cells.append(d.model)
    if d.cost.input_tokens or d.cost.output_tokens:
        cells.append(f"in={d.cost.input_tokens} out={d.cost.output_tokens}")
    if d.cost.total_cost or d.model:
        cells.append(_money(d.cost.total_cost))
    if d.latency_ms:
        cells.append(f"{d.latency_ms}ms")
    sup = d.supervision
    if sup and sup.verdict:
        cells.append(sup.verdict)
        cells.append(f"executed={d.executed}")
        if sup.retry_number is not None:
            cells.append(f"retry={sup.retry_number}")
    elif d.outcome:
        cells.append(d.outcome)
    if d.error:
        cells.append(f"error={_short(d.error, 80)}")
    return "  " + "  ".join(str(c) for c in cells)


def _verbose_lines(d, provenance: Optional[dict]) -> List[str]:
    out: List[str] = []

    def add(label: str, val: Any) -> None:
        if val is not None and val != "" and val != []:
            out.append(f"        {label}: {val}")

    add("timestamp", d.timestamp)  # the decision's actual time (verdict/check time for a supervised decision)
    if d.supervision and d.supervision.executed_at_unix:
        add("executed at (unix)", d.supervision.executed_at_unix)
    add("routing reason", d.routing_reason)
    add("tool args", d.tool_args_ref)
    c = d.cost
    if c.input_cost or c.output_cost or c.model_cost or c.tool_cost:
        add("cost breakdown",
            f"input {_money(c.input_cost)}  output {_money(c.output_cost)}  "
            f"model {_money(c.model_cost)}  tool {_money(c.tool_cost)}")
    sup = d.supervision
    if sup:
        add("constraint", sup.applicable_constraint)
        add("scope", sup.scope)
        add("transaction", sup.transaction_id)
        if d.agent_id:
            add("agent", d.agent_id)
        add("proposed", sup.proposed_option or sup.proposed_kind)
        add("proposed params", sup.proposed_fields_redacted)  # secret-like keys already masked
        add("proposed fingerprint", sup.proposed_fingerprint)
        add("rejection reason", sup.rejection_reason)
        add("suggested evidence", sup.suggested_from_evidence)
        add("evidence trust", sup.evidence_trust)
        add("evidence provider", sup.evidence_provider_id)
        add("evidence subject", sup.evidence_subject)
        add("evidence ref", sup.trusted_evidence_ref)
        add("evidence fingerprint", sup.evidence_fingerprint)
        add("evidence source", sup.evidence_source)
        add("evidence version", sup.evidence_version)
        if sup.evidence_observed_at_unix:
            add("evidence observed_at", sup.evidence_observed_at_unix)
        if sup.evidence_expires_at_unix:
            add("evidence expires_at", sup.evidence_expires_at_unix)
        add("auth state", sup.auth_state)
        add("idempotency key", sup.idempotency_key)
        if sup.issued_at_unix:
            add("issued at (unix)", sup.issued_at_unix)
        if sup.consumed_at_unix:
            add("consumed at (unix)", sup.consumed_at_unix)
    v = d.verification
    if v:
        add("verification", f"method={v.method} passed={v.passed} score={v.score}")
    if provenance and isinstance(provenance, dict):
        pd = (provenance.get("decisions") or {}).get(d.id)
        if pd:
            add("reported by runtime", pd.get("reported"))
            add("derived by ARK", pd.get("derived"))
    return out


def format_run(result, verbose: bool = False, provenance: Optional[dict] = None) -> str:
    """Return the human-readable report for a canonical RunResult."""
    L: List[str] = []
    add = L.append

    add("ARK RUN REPORT")
    add("")

    # ---- Run ----
    add("Run")
    add(f"  id           {result.run_id}")
    status = "success" if result.success else "failed"
    if result.termination_reason:
        status += f" ({result.termination_reason})"
    add(f"  status       {status}")
    if result.task:
        add(f"  task         {_short(result.task, 100)}")
    models = sorted({d.model for d in result.decisions if d.model})
    add(f"  model(s)     {', '.join(models) if models else '-'}")
    add(f"  decisions    {len(result.decisions)}")
    add(f"  tool calls   {result.tools.calls}")
    add("")

    # ---- Cost (factual attribution; no ON/OFF comparison, no 'ARK saved/cost X') ----
    add("Cost")
    add(f"  total        {_money(result.total_cost)}")
    add(f"  tokens       {result.total_tokens}")
    add(f"  latency      {result.total_latency_ms} ms")
    if result.cost_by_model:
        add(f"  by model     {_kv_money(result.cost_by_model)}")
    if result.cost_by_tool:
        add(f"  by tool      {_kv_money(result.cost_by_tool)}")
    if result.cost_by_action:
        add(f"  by action    {_kv_money(result.cost_by_action)}")
    if result.cost_by_supervision:
        add(f"  by supervis. {_kv_money(result.cost_by_supervision)}  (cost of decisions carrying a verdict)")
    add("")

    # ---- Supervision ----
    sup = result.supervision
    add("Supervision")
    add(f"  enabled      {str(sup.enabled).lower()}")
    if sup.enabled:
        add(f"  interventions {sup.interventions}")
        add(f"  verdicts     {_kv_int(sup.by_verdict)}")
    add("")

    # ---- Decision Trace (in order) ----
    add("Decision Trace")
    if not result.decisions:
        add("  (no decisions recorded)")
    for d in result.decisions:
        add(_trace_line(d))
        if verbose:
            L.extend(_verbose_lines(d, provenance))

    if not verbose:
        add("")
        add("Run report(verbose=True) for per-decision provenance, cost breakdown, and evidence.")
    return "\n".join(L)
