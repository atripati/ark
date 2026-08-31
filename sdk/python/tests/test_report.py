"""run.report() / format_run tests: the report is built only from the canonical RunResult,
shows the reject->allow story with executed flags, totals match the canonical result, missing
fields do not crash, verbose exposes richer fields + provenance, and no secrets can appear.
"""
import json
import math

import ark
from ark import ARK, format_run
from ark.models import RunResult
from ark.report import _money


def _observe_run():
    with ark.trace("rank things") as run:
        run.record(action="tool_call", tool="search", model="gpt-4o-mini",
                   input_tokens=449, output_tokens=16, latency_ms=620, outcome="success")
        run.record(action="complete", model="gpt-4o", input_tokens=882, output_tokens=186,
                   latency_ms=1400, outcome="stop")
    return run


def _reject_then_allow_run():
    ev = {"requested_rank": 2, "evidence_complete": True,
          "options": [{"id": "A", "price": 100}, {"id": "B", "price": 200}, {"id": "C", "price": 300}]}
    with ARK(supervision="experimental").trace("book flight", task_type="booking") as run:
        v1 = run.check(proposed_action={"option": "C"}, constraint="rank", evidence=ev, tool="book")
        run.record(action="tool_call", tool="book", tool_args={"option": "C"},
                   outcome="rejected", executed=False, of=v1)
        v2 = run.check(proposed_action={"option": "B"}, constraint="rank", evidence=ev, tool="book")
        run.record(action="tool_call", tool="book", tool_args={"option": "B"}, model="gpt-4o",
                   input_tokens=100, output_tokens=20, outcome="success", of=v2)
    return run


def test_report_observe_only():
    r = _observe_run().result
    txt = format_run(r)
    assert "ARK RUN REPORT" in txt
    assert "decision_001" in txt and "decision_002" in txt
    assert "enabled      false" in txt  # supervision off


def test_report_immediate_allow():
    ev = {"requested_rank": 2, "evidence_complete": True,
          "options": [{"id": "A", "price": 100}, {"id": "B", "price": 200}]}
    with ARK(supervision="experimental").trace("book") as run:
        v = run.check(proposed_action={"option": "B"}, constraint="rank", evidence=ev, tool="book")
        assert v.allowed
        run.record(action="tool_call", tool="book", model="gpt-4o", input_tokens=100,
                   output_tokens=20, of=v)
    txt = format_run(run.result)
    assert "ALLOW" in txt and "executed=True" in txt
    assert "REJECT" not in txt


def test_report_reject_then_allow_shows_executed_flags():
    r = _reject_then_allow_run().result
    txt = format_run(r)
    # decision order/ids match the canonical result
    assert [d.id for d in r.decisions] == ["decision_001", "decision_002"]
    # isolate the decision-trace lines (the Cost summary also mentions ALLOW/REJECT)
    lines = txt.splitlines()
    rej = next(l for l in lines if "decision_" in l and "REJECT" in l and "executed" in l)
    allow = next(l for l in lines if "decision_" in l and "ALLOW" in l and "executed" in l)
    assert "executed=False" in rej and "option=C" in rej   # rejected proposal, not executed
    assert "executed=True" in allow and "option=B" in allow  # allowed proposal, executed


def test_report_cost_total_matches_canonical():
    r = _observe_run().result
    txt = format_run(r)
    assert _money(r.total_cost) in txt
    # every by-model line reconciles to the canonical mapping
    assert abs(sum(r.cost_by_model.values()) - r.total_cost) < 1e-12


def test_report_verbose_exposes_richer_fields():
    run = _reject_then_allow_run()
    txt = format_run(run.result, verbose=True, provenance=run.provenance)
    assert "rejection reason" in txt
    assert "suggested evidence" in txt
    assert "cost breakdown" in txt
    assert "reported by runtime" in txt and "derived by ARK" in txt


def test_report_missing_optional_fields_no_crash():
    r = RunResult.from_dict({
        "run_id": "x", "success": True,
        "decisions": [{"id": "decision_001", "sequence": 1, "decision_type": "complete"}],
    })
    txt = format_run(r)              # must not raise on sparse fields
    assert "decision_001" in txt
    assert "ARK RUN REPORT" in txt


def test_report_empty_run_no_crash():
    r = RunResult.from_dict({"run_id": "x", "success": True, "decisions": []})
    txt = format_run(r)
    assert "(no decisions recorded)" in txt


def test_report_has_no_secrets():
    run = _reject_then_allow_run()
    txt = format_run(run.result, verbose=True, provenance=run.provenance).lower()
    for bad in ("sk-proj", "sk-ant", "ghp_", "bearer ", "authorization:", "api_key="):
        assert bad not in txt


def test_run_report_prints(capsys):
    run = _observe_run()
    run.report()
    out = capsys.readouterr().out
    assert "ARK RUN REPORT" in out and "decision_001" in out


def test_result_report_and_serializers():
    r = _observe_run().result
    d = r.to_dict()
    assert d["run_id"] == r.run_id
    parsed = json.loads(r.to_json())
    assert parsed["run_id"] == r.run_id
    assert math.isclose(parsed["total_cost"], r.total_cost)
    # RunResult.report also prints (no provenance needed)
    r.report()  # should not raise
