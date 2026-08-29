"""External-agent `trace` session tests: an arbitrary Python loop attaches ARK, reports
decisions, optionally gates actions, and receives the canonical RunResult with the full
proposal -> supervision -> retry -> execution -> outcome -> cost chain.
"""
import math

import pytest

import ark
from ark import ARK, ArkSupervisionDisabled, RunResult
from ark.session import Verdict


def approx(a, b):
    return math.isclose(a, b, abs_tol=1e-9)


# ---- observe-only ---------------------------------------------------------------

def test_observe_only_produces_canonical_runresult():
    with ark.trace(task="rank web frameworks") as run:
        run.record(action="tool_call", tool="search", model="gpt-4o-mini",
                   input_tokens=449, output_tokens=16, latency_ms=600, outcome="success")
        run.record(action="complete", model="gpt-4o",
                   input_tokens=882, output_tokens=186, latency_ms=1400, outcome="complete")
    r = run.result
    assert isinstance(r, RunResult)
    assert r.success is True
    assert [d.id for d in r.decisions] == ["decision_001", "decision_002"]
    assert [d.sequence for d in r.decisions] == [1, 2]
    # no supervision was used
    assert all(d.supervision is None for d in r.decisions)
    assert r.supervision.enabled is False


def test_cost_is_derived_from_reported_tokens_and_model():
    with ark.trace(task="x") as run:
        run.record(action="complete", model="gpt-4o", input_tokens=882, output_tokens=186)
    d = run.result.decisions[0]
    # gpt-4o: 2.50/M in, 10.00/M out
    assert approx(d.cost.input_cost, 882 * 2.50 / 1_000_000)
    assert approx(d.cost.output_cost, 186 * 10.0 / 1_000_000)
    assert approx(d.cost.total_cost, d.cost.input_cost + d.cost.output_cost)
    assert approx(run.result.total_cost, d.cost.total_cost)


def test_developer_supplied_cost_is_not_overwritten():
    with ark.trace(task="x") as run:
        run.record(action="complete", model="mystery-model", input_tokens=100,
                   output_tokens=50, cost=0.5)
    assert approx(run.result.decisions[0].cost.total_cost, 0.5)


def test_provenance_separates_reported_from_derived():
    with ark.trace(task="x") as run:
        run.record(action="complete", model="gpt-4o", input_tokens=882, output_tokens=186)
    prov = run.provenance
    assert prov["origin"] == "external_session"
    pv = prov["decisions"]["decision_001"]
    # reported facts came from the developer; ARK never claims it observed them
    for f in ("action", "model", "input_tokens", "output_tokens"):
        assert f in pv["reported"]
    # cost + ids are ARK-derived, not reported
    assert "cost" in pv["derived"]
    assert "cost" not in pv["reported"]


def test_cost_aggregates_reconcile():
    with ark.trace(task="x") as run:
        run.record(action="tool_call", tool="search", model="gpt-4o-mini",
                   input_tokens=449, output_tokens=16)
        run.record(action="complete", model="gpt-4o", input_tokens=882, output_tokens=186)
    r = run.result
    assert approx(sum(d.cost.total_cost for d in r.decisions), r.total_cost)
    assert approx(sum(r.cost_by_model.values()), r.total_cost)
    assert approx(sum(r.cost_by_action.values()), r.total_cost)


# ---- supervised chain -----------------------------------------------------------

def test_full_supervised_chain_reject_then_allow():
    ev = {"requested_rank": 2, "evidence_complete": True,
          "options": [{"id": "A", "price": 163, "is_direct": True},
                      {"id": "B", "price": 290, "is_direct": True}]}
    with ARK(supervision="experimental").trace(task="book 2nd-cheapest", task_type="booking") as run:
        v1 = run.check(proposed_action={"option": "A"}, constraint="rank", evidence=ev, tool="book")
        assert isinstance(v1, Verdict)
        assert v1.verdict == "REJECT" and v1.allowed is False
        assert v1.suggested == "B" and v1.retry_number == 0

        v2 = run.check(proposed_action={"option": "B"}, constraint="rank", evidence=ev, tool="book")
        assert v2.verdict == "ALLOW" and v2.allowed is True
        assert v2.retry_number == 1                      # retry state advanced IN GO
        run.record(action="tool_call", tool="book", model="gpt-4o",
                   input_tokens=882, output_tokens=186, of=v2)

    r = run.result
    # decision_001 = rejected proposal (not executed, no cost); decision_002 = allowed+executed
    assert [d.id for d in r.decisions] == ["decision_001", "decision_002"]
    rej, allw = r.decisions
    assert rej.supervision.verdict == "REJECT" and rej.executed is False
    assert rej.supervision.suggested_from_evidence == "B"
    assert approx(rej.cost.total_cost, 0.0)             # a rejected proposal costs nothing
    assert allw.supervision.verdict == "ALLOW" and allw.executed is True
    assert allw.model == "gpt-4o" and allw.cost.total_cost > 0
    # the executed telemetry landed on the SAME record as the ALLOW verdict (of=v2)
    assert allw.supervision.executed is True
    assert r.supervision.enabled is True and r.supervision.interventions == 1
    assert r.supervision.by_verdict == {"REJECT": 1, "ALLOW": 1}


def test_retry_budget_recovery_exhausted_is_authoritative_in_go():
    # keep proposing the wrong (rank-1) option; ARK must eventually RECOVERY_EXHAUSTED.
    ev = {"requested_rank": 2, "evidence_complete": True,
          "options": [{"id": "A", "price": 163, "is_direct": True},
                      {"id": "B", "price": 290, "is_direct": True}]}
    verdicts = []
    with ARK(supervision="experimental").trace(task="book", budget=3) as run:
        for _ in range(6):
            v = run.check(proposed_action={"option": "A"}, constraint="rank", evidence=ev, tool="book")
            verdicts.append(v.verdict)
            if v.verdict == "RECOVERY_EXHAUSTED":
                break
    # budget=3 -> three REJECTs (retry 0,1,2) then RECOVERY_EXHAUSTED at retry 3
    assert verdicts == ["REJECT", "REJECT", "REJECT", "RECOVERY_EXHAUSTED"]


def test_of_verdict_links_execution_to_proposal():
    ev = {"requested_rank": 2, "evidence_complete": True,
          "options": [{"id": "A", "price": 163}, {"id": "B", "price": 290}]}
    with ARK(supervision="experimental").trace(task="book") as run:
        v = run.check(proposed_action={"option": "B"}, constraint="rank", evidence=ev, tool="book")
        did = run.record(action="tool_call", tool="book", model="gpt-4o",
                         input_tokens=100, output_tokens=20, of=v)
    assert did == v.decision_id                         # same decision, unambiguous chain
    assert len(run.result.decisions) == 1


# ---- guardrails -----------------------------------------------------------------

def test_check_requires_experimental_supervision():
    with ark.trace(task="x") as run:  # supervision defaults off
        with pytest.raises(ArkSupervisionDisabled):
            run.check(proposed_action={"option": "A"}, constraint="rank", evidence={})


def test_result_unavailable_before_finish():
    from ark.errors import ArkError
    run = ark.trace(task="x")
    with pytest.raises(ArkError):
        _ = run.result


def test_exception_in_block_still_finishes_with_failure():
    class Boom(Exception):
        pass
    run = ark.trace(task="x")
    with pytest.raises(Boom):
        with run:
            run.record(action="complete", model="gpt-4o", input_tokens=10, output_tokens=5)
            raise Boom()
    # the trace still closed cleanly and produced a result marked unsuccessful
    assert run.result.success is False
    assert "exception" in (run.result.termination_reason or "")


def test_providers_report_status_not_secrets():
    with ark.trace(task="x") as run:
        run.record(action="complete", model="gpt-4o", input_tokens=10, output_tokens=5)
    r = run.result
    # external mode: provider is "reported" (ARK didn't call it), never a key
    assert r.providers.get("openai") == "reported"
    blob = str(r.raw).lower()
    for bad in ("sk-proj", "sk-ant", "ghp_", "bearer ", "authorization:", "api_key="):
        assert bad not in blob
