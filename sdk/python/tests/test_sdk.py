import math

import pytest

from ark import ARK, ArkBridgeError, ArkSupervisionDisabled, RunResult, DecisionRecord
from ark.bridge import SubprocessBridge


def approx(a, b):
    return math.isclose(a, b, abs_tol=1e-9)


def test_bridge_run_to_runresult():
    r = ARK().run(task="find the top Python web frameworks on GitHub")
    assert isinstance(r, RunResult)
    assert r.success is True
    assert len(r.decisions) >= 1
    assert all(isinstance(d, DecisionRecord) for d in r.decisions)


def test_python_parsing_fields():
    r = ARK().run(task="rank things")
    assert r.run_id and r.total_cost >= 0 and r.total_tokens >= 0
    assert isinstance(r.cost_by_model, dict) and isinstance(r.cost_by_action, dict)
    d0 = r.decisions[0]
    assert d0.model and d0.action and isinstance(d0.cost.total_cost, float)


def test_decision_ordering_stable():
    r = ARK().run(task="rank things")
    ids = [d.id for d in r.decisions]
    assert ids == [f"decision_{i+1:03d}" for i in range(len(ids))]
    assert [d.sequence for d in r.decisions] == list(range(1, len(ids) + 1))


def test_cost_totals_reconcile():
    r = ARK().run(task="rank things")
    assert approx(sum(d.cost.total_cost for d in r.decisions), r.total_cost)
    assert approx(sum(r.cost_by_model.values()), r.total_cost)
    assert approx(sum(r.cost_by_action.values()), r.total_cost)
    # by_tool is the tool-associated subset
    tool_sum = sum(d.cost.total_cost for d in r.decisions if d.tool)
    assert approx(sum(r.cost_by_tool.values()), tool_sum)


def test_routing_from_actual_router():
    r = ARK().run(task="rank things")
    # real router decisions flowed through: models + reasons are populated per decision
    assert any(d.routing_reason for d in r.decisions)
    assert any(d.model for d in r.decisions)
    assert r.routing.by_model  # run-level routing summary


def test_supervision_records_survive_bridge():
    ark = ARK(supervision="experimental")
    ev = {"requested_rank": 2, "evidence_complete": True,
          "options": [{"id": "A", "price": 163, "is_direct": True}, {"id": "B", "price": 290}]}
    r1 = ark.supervise(constraint="rank", proposed={"option": "A"}, evidence=ev, retry_count=0)
    assert r1.verdict == "REJECT" and r1.record is not None
    assert r1.suggested_from_evidence == "B" and r1.record.executed is False
    r2 = ark.supervise(constraint="rank", proposed={"option": "B"}, evidence=ev, retry_count=1)
    assert r2.verdict == "ALLOW" and r2.allowed is True


def test_optional_fields_stay_optional():
    r = ARK().run(task="rank things")
    # a run without supervision -> decision.supervision is None; run.cost_by_supervision None
    assert all(d.supervision is None for d in r.decisions)
    assert r.cost_by_supervision is None
    assert r.supervision.enabled is False


def test_errors_map_cleanly():
    br = SubprocessBridge()
    with pytest.raises(ArkBridgeError):
        br.call({"kind": "nope"})  # unknown kind -> bridge error
    with pytest.raises(ArkBridgeError):
        SubprocessBridge(binary="/nonexistent/ark-bridge").call({"kind": "run", "task": "x"})


def test_secrets_never_returned():
    r = ARK().run(task="rank things")
    blob = str(r.raw).lower()
    # actual secret VALUE patterns must never appear (field names like input_tokens are fine)
    for bad in ("sk-proj", "sk-ant", "ghp_", "bearer ", "authorization:", "api_key="):
        assert bad not in blob, f"telemetry leaked a secret pattern {bad!r}"
    # providers report status only — never the key material
    assert r.providers.get("openai") == "configured"
    assert all(v in ("configured", "absent") for v in r.providers.values())


def test_supervision_off_by_default():
    with pytest.raises(ArkSupervisionDisabled):
        ARK().supervise(constraint="rank", proposed={"option": "A"}, evidence={})


def test_runtime_result_stable_across_sdk_calls():
    # SDK usage is read-only over the runtime: repeated runs yield the same structure.
    a = ARK().run(task="rank things")
    b = ARK().run(task="rank things")
    assert a.success == b.success
    assert [d.action for d in a.decisions] == [d.action for d in b.decisions]
    assert approx(a.total_cost, b.total_cost)
