"""End-to-end fail-closed proofs for the supervision kernel, driven through the REAL Go bridge
(the conftest builds it). Every test here asserts that an unsafe path does NOT produce ALLOW.
"""
import time

import pytest

import ark
from ark import ARK
from ark.errors import ArkBridgeError, ArkSupervisionError

EV = {"requested_rank": 2, "evidence_complete": True,
      "options": [{"id": "A", "price": 163, "is_direct": True},
                  {"id": "B", "price": 290, "is_direct": True}]}


# ---------- fail-closed configuration / input validation ----------

def test_unknown_constraint_fails_closed():
    with ARK(supervision="experimental").trace("t") as run:
        with pytest.raises(ArkBridgeError):
            run.check(proposed_action={"option": "A"}, constraint="refund_limit",
                      evidence=EV, scope="order-1", tool="book")


def test_missing_scope_raises_client_side():
    with ARK(supervision="experimental").trace("t") as run:
        with pytest.raises(ArkSupervisionError):
            run.check(proposed_action={"option": "A"}, constraint="rank", evidence=EV, scope="  ")


def test_typo_evidence_field_rejected():
    bad = {"requestedRank": 2, "evidence_complete": True, "options": [{"id": "A", "price": 1}]}
    with ARK(supervision="experimental").trace("t") as run:
        with pytest.raises(ArkBridgeError):
            run.check(proposed_action={"option": "A"}, constraint="rank", evidence=bad, scope="order-1")


def test_missing_requested_rank_rejected():
    bad = {"evidence_complete": True, "options": [{"id": "A", "price": 1}]}
    with ARK(supervision="experimental").trace("t") as run:
        with pytest.raises(ArkBridgeError):
            run.check(proposed_action={"option": "A"}, constraint="rank", evidence=bad, scope="order-1")


def test_empty_evidence_rejected():
    with ARK(supervision="experimental").trace("t") as run:
        with pytest.raises(ArkBridgeError):
            run.check(proposed_action={"option": "A"}, constraint="rank", evidence={}, scope="order-1")


def test_duplicate_option_id_rejected():
    bad = {"requested_rank": 2, "evidence_complete": True,
           "options": [{"id": "A", "price": 1}, {"id": "A", "price": 2}]}
    with ARK(supervision="experimental").trace("t") as run:
        with pytest.raises(ArkBridgeError):
            run.check(proposed_action={"option": "A"}, constraint="rank", evidence=bad, scope="order-1")


# ---------- insufficient evidence -> REQUIRE_EVIDENCE, never ALLOW ----------

@pytest.mark.parametrize("mutate,expect", [
    (lambda e: {**e, "evidence_complete": False}, "REQUIRE_EVIDENCE"),       # incomplete
    (lambda e: {**e, "requested_rank": 9}, "REQUIRE_EVIDENCE"),               # rank exceeds tiers
])
def test_unverifiable_evidence_requires_evidence(mutate, expect):
    with ARK(supervision="experimental").trace("t") as run:
        v = run.check(proposed_action={"option": "A"}, constraint="rank",
                      evidence=mutate(EV), scope="order-1", tool="book")
    assert v.verdict == expect and v.allowed is False


def test_proposed_option_absent_requires_evidence():
    with ARK(supervision="experimental").trace("t") as run:
        v = run.check(proposed_action={"option": "ZZZ"}, constraint="rank",
                      evidence=EV, scope="order-1", tool="book")
    assert v.verdict == "REQUIRE_EVIDENCE" and v.allowed is False


# ---------- transaction/entity binding (INVARIANT 5) ----------

def test_two_entities_have_independent_retry_budgets():
    with ARK(supervision="experimental").trace("t", budget=2) as run:
        # exhaust entity A (rank-1 proposal always REJECTs)
        seen = []
        for _ in range(4):
            seen.append(run.check(proposed_action={"option": "A"}, constraint="rank",
                                  evidence=EV, scope="order-A", tool="book").verdict)
            if seen[-1] == "RECOVERY_EXHAUSTED":
                break
        assert "RECOVERY_EXHAUSTED" in seen
        # entity B starts fresh: its first wrong proposal is a REJECT, not RECOVERY_EXHAUSTED
        vb = run.check(proposed_action={"option": "A"}, constraint="rank",
                       evidence=EV, scope="order-B", tool="book")
    assert vb.verdict == "REJECT"


# ---------- replay / idempotency / no-execute-on-non-ALLOW ----------

def test_cannot_record_execution_of_rejected_action():
    with ARK(supervision="experimental").trace("t") as run:
        v = run.check(proposed_action={"option": "A"}, constraint="rank",
                      evidence=EV, scope="order-1", tool="book")  # rank-1 -> REJECT
        assert v.verdict == "REJECT"
        with pytest.raises(ArkBridgeError):
            run.record(action="tool_call", tool="book", executed=True, of=v)


def test_execution_requires_executed_action():
    # D2: confirming execution of an ALLOW WITHOUT presenting the actual action -> refused.
    with ARK(supervision="experimental").trace("t") as run:
        v = run.check(proposed_action={"option": "B"}, constraint="rank",
                      evidence=EV, scope="order-1", tool="book")  # ALLOW
        with pytest.raises(ArkBridgeError):
            run.record(action="tool_call", tool="book", executed=True, of=v)  # no executed_action


def test_duplicate_execution_is_refused():
    with ARK(supervision="experimental").trace("t") as run:
        v = run.check(proposed_action={"option": "B"}, constraint="rank",
                      evidence=EV, scope="order-1", tool="book")  # ALLOW
        run.record(action="tool_call", tool="book", model="gpt-4o", input_tokens=10,
                   output_tokens=2, executed=True, of=v, executed_action={"option": "B"})
        with pytest.raises(ArkBridgeError):
            run.record(action="tool_call", tool="book", executed=True, of=v,
                       executed_action={"option": "B"})


def test_modified_action_option_is_refused():
    # check B, execute A -> refused.
    with ARK(supervision="experimental").trace("t") as run:
        v = run.check(proposed_action={"option": "B"}, constraint="rank",
                      evidence=EV, scope="order-1", tool="book")  # ALLOW for B
        with pytest.raises(ArkBridgeError):
            run.record(action="tool_call", tool="book", executed=True, of=v,
                       executed_action={"option": "A"})  # executed a different option


def test_modified_action_field_is_refused():
    # same tool/option, changed customer/resource field -> refused.
    with ARK(supervision="experimental").trace("t") as run:
        v = run.check(proposed_action={"option": "B", "fields": {"customer": "91"}},
                      constraint="rank", evidence=EV, scope="order-1", tool="book")
        with pytest.raises(ArkBridgeError):
            run.record(action="tool_call", tool="book", executed=True, of=v,
                       executed_action={"option": "B", "fields": {"customer": "92"}})


def test_python_dict_mutated_after_check_is_refused():
    # the caller mutates the action dict after check, then executes the mutated one -> refused.
    with ARK(supervision="experimental").trace("t") as run:
        action = {"option": "B", "fields": {"customer": "91"}}
        v = run.check(proposed_action=dict(action), constraint="rank", evidence=EV,
                      scope="order-1", tool="book")
        action["fields"]["customer"] = "92"  # mutate after authorization
        with pytest.raises(ArkBridgeError):
            run.record(action="tool_call", tool="book", executed=True, of=v, executed_action=action)


def test_matching_action_is_accepted():
    with ARK(supervision="experimental").trace("t") as run:
        v = run.check(proposed_action={"option": "B"}, constraint="rank",
                      evidence=EV, scope="order-1", tool="book")
        run.record(action="tool_call", tool="book", model="gpt-4o", input_tokens=10,
                   output_tokens=2, executed=True, of=v, executed_action={"option": "B"})
    assert run.result.decisions[0].supervision.executed is True


def test_action_binding_is_key_order_independent():
    # semantically identical action, different dict key order -> still matches.
    with ARK(supervision="experimental").trace("t") as run:
        v = run.check(proposed_action={"option": "B", "fields": {"a": 1, "b": 2}},
                      constraint="rank", evidence=EV, scope="order-1", tool="book")
        run.record(action="tool_call", tool="book", executed=True, of=v,
                   executed_action={"fields": {"b": 2, "a": 1}, "option": "B"})
    assert run.result.decisions[0].supervision.executed is True


# ---------- decision timestamps (D3) ----------

def test_decision_timestamps_are_real_and_utc():
    with ARK(supervision="experimental").trace("t") as run:
        v1 = run.check(proposed_action={"option": "A"}, constraint="rank",
                       evidence=EV, scope="order-1", tool="book")  # REJECT
        run.record(action="tool_call", tool="book", executed=False, of=v1)
        v2 = run.check(proposed_action={"option": "B"}, constraint="rank",
                       evidence=EV, scope="order-1", tool="book")  # ALLOW
        run.record(action="tool_call", tool="book", model="gpt-4o", input_tokens=10,
                   output_tokens=2, executed=True, of=v2, executed_action={"option": "B"})
    ds = run.result.decisions
    assert len(ds) == 2
    for d in ds:
        assert d.timestamp, "each decision must carry its own timestamp"
        assert d.timestamp.endswith("Z") or "+00:00" in d.timestamp, "timestamps must be UTC"
    # the two verdicts were evaluated at (weakly) increasing times, not collapsed to finish time
    assert ds[0].timestamp <= ds[1].timestamp
    # execution time is recorded distinctly from the verdict time
    execd = ds[1].supervision.executed_at_unix
    assert execd and execd > 0


# ---------- pre-execution consume gate / lifecycle ----------

def test_consume_then_execute_then_record():
    with ARK(supervision="experimental").trace("t") as run:
        v = run.check(proposed_action={"option": "B"}, constraint="rank", evidence=EV,
                      scope="order-1", transaction="txn-1", agent_id="agent-7", tool="book")
        cleared = run.consume(v, executed_action={"option": "B"})
        assert cleared.cleared is True
        assert cleared.idempotency_key  # forwardable to a cooperating external API
        run.record(action="tool_call", tool="book", model="gpt-4o", input_tokens=10,
                   output_tokens=2, executed=True, of=v, executed_action={"option": "B"})
    sup = run.result.decisions[0].supervision
    assert sup.auth_state == "COMPLETED"
    assert sup.transaction_id == "txn-1"
    assert sup.issued_at_unix and sup.consumed_at_unix and sup.executed_at_unix
    assert run.result.decisions[0].agent_id == "agent-7"


def test_consume_replay_refused():
    with ARK(supervision="experimental").trace("t") as run:
        v = run.check(proposed_action={"option": "B"}, constraint="rank", evidence=EV,
                      scope="order-1", tool="book")
        assert run.consume(v, executed_action={"option": "B"}).cleared
        with pytest.raises(ArkBridgeError):
            run.consume(v, executed_action={"option": "B"})  # second consume -> replay refused


def test_consume_action_mismatch_refused():
    with ARK(supervision="experimental").trace("t") as run:
        v = run.check(proposed_action={"option": "B"}, constraint="rank", evidence=EV,
                      scope="order-1", tool="book")
        with pytest.raises(ArkBridgeError):
            run.consume(v, executed_action={"option": "A"})  # different action


def test_consume_non_allow_refused():
    with ARK(supervision="experimental").trace("t") as run:
        v = run.check(proposed_action={"option": "A"}, constraint="rank", evidence=EV,
                      scope="order-1", tool="book")  # rank-1 -> REJECT
        with pytest.raises(ArkBridgeError):
            run.consume(v, executed_action={"option": "A"})


def test_consume_blocks_stale_before_execution():
    # ATTACK 1: fresh at check, expired by consume time -> blocked BEFORE the side effect.
    import time as _t
    now = int(_t.time())
    ev = dict(EV)
    ev["meta"] = {"observed_at_unix": now, "expires_at_unix": now + 1, "source": "svc"}
    with ARK(supervision="experimental").trace("t") as run:
        v = run.check(proposed_action={"option": "B"}, constraint="rank", evidence=ev,
                      scope="order-1", tool="book")
        assert v.allowed  # fresh at check
        _t.sleep(1.6)     # the freshness window elapses before use
        cleared = run.consume(v, executed_action={"option": "B"})
    assert cleared.cleared is False and cleared.requires_recheck is True


# ---------- transaction isolation ----------

def test_transaction_isolates_retry_same_entity():
    with ARK(supervision="experimental").trace("t", budget=2) as run:
        seen = []
        for _ in range(4):
            r = run.check(proposed_action={"option": "A"}, constraint="rank", evidence=EV,
                          scope="customer-123", transaction="txn-A", tool="book")
            seen.append(r.verdict)
            if r.verdict == "RECOVERY_EXHAUSTED":
                break
        assert "RECOVERY_EXHAUSTED" in seen
        # a different transaction for the SAME customer starts fresh
        rb = run.check(proposed_action={"option": "A"}, constraint="rank", evidence=EV,
                       scope="customer-123", transaction="txn-B", tool="book")
    assert rb.verdict == "REJECT"


# ---------- future timestamp ----------

def test_future_evidence_timestamp_never_fresh():
    ev = dict(EV)
    ev["meta"] = {"observed_at_unix": 9_000_000_000}  # ~year 2255
    with ARK(supervision="experimental").trace("t") as run:
        v = run.check(proposed_action={"option": "B"}, constraint="rank", evidence=ev,
                      scope="order-1", tool="book", max_evidence_age=60)
    assert v.verdict == "REQUIRE_EVIDENCE"  # a future timestamp can never look fresh


# ---------- forensic audit ----------

def test_audit_has_forensic_fields_and_no_secrets():
    from ark.report import format_run
    ev = dict(EV)
    ev["meta"] = {"evidence_id": "snap-9", "source": "inventory", "version": "v3",
                  "observed_at_unix": int(__import__("time").time())}
    with ARK(supervision="experimental").trace("t") as run:
        v = run.check(proposed_action={"option": "B", "fields": {"amount": 4700, "api_key": "sk-SECRET"}},
                      constraint="rank", evidence=ev, scope="customer-91", transaction="txn-42",
                      agent_id="agent-27", tool="refund")
        run.consume(v, executed_action={"option": "B", "fields": {"amount": 4700, "api_key": "sk-SECRET"}})
        run.record(action="tool_call", tool="refund", model="gpt-4o", input_tokens=5, output_tokens=1,
                   executed=True, of=v, executed_action={"option": "B", "fields": {"amount": 4700, "api_key": "sk-SECRET"}})
    sup = run.result.decisions[0].supervision
    # reconstructable identity + lifecycle
    assert sup.transaction_id == "txn-42" and run.result.decisions[0].agent_id == "agent-27"
    assert sup.idempotency_key and sup.issued_at_unix and sup.consumed_at_unix and sup.executed_at_unix
    assert sup.evidence_source == "inventory" and sup.trusted_evidence_ref == "snap-9"
    # the non-secret parameter survives (amount), the secret does not
    assert "4700" in (sup.proposed_fields_redacted or "")
    txt = format_run(run.result, verbose=True)
    assert "sk-SECRET" not in txt and "4700" in txt


# ---------- freshness ----------

def test_stale_evidence_requires_evidence():
    stale = dict(EV)
    stale["meta"] = {"observed_at_unix": int(time.time()) - 3600, "source": "inventory-svc"}
    with ARK(supervision="experimental").trace("t") as run:
        v = run.check(proposed_action={"option": "B"}, constraint="rank",
                      evidence=stale, scope="order-1", tool="book", max_evidence_age=60)
    assert v.verdict == "REQUIRE_EVIDENCE"


def test_fresh_evidence_evaluates_normally():
    fresh = dict(EV)
    fresh["meta"] = {"observed_at_unix": int(time.time()), "source": "inventory-svc", "version": "v7"}
    with ARK(supervision="experimental").trace("t") as run:
        v = run.check(proposed_action={"option": "B"}, constraint="rank",
                      evidence=fresh, scope="order-1", tool="book", max_evidence_age=3600)
    assert v.verdict == "ALLOW"


# ---------- audit record hardening ----------

def test_audit_record_carries_structured_provenance():
    fresh = dict(EV)
    fresh["meta"] = {"evidence_id": "snap-42", "source": "inventory-svc", "version": "v7",
                     "observed_at_unix": int(time.time())}
    with ARK(supervision="experimental").trace("t") as run:
        v = run.check(proposed_action={"option": "A"}, constraint="rank",
                      evidence=fresh, scope="order-77", tool="book")  # REJECT
    sup = run.result.decisions[0].supervision
    assert sup.scope == "order-77"
    assert sup.proposed_option == "A"
    assert sup.proposed_fingerprint and sup.evidence_fingerprint
    assert sup.trusted_evidence_ref == "snap-42"   # the caller id, not a constant placeholder
    assert sup.evidence_source == "inventory-svc" and sup.evidence_version == "v7"


def test_report_verbose_shows_audit_and_no_secret():
    from ark.report import format_run
    with ARK(supervision="experimental").trace("t") as run:
        v = run.check(proposed_action={"option": "A"}, constraint="rank",
                      evidence=EV, scope="order-1", tool="book")
        run.record(action="tool_call", tool="book", tool_args={"option": "A", "api_key": "sk-SECRET"},
                   executed=False, of=v)
    txt = format_run(run.result, verbose=True)
    assert "scope" in txt and "fingerprint" in txt
    assert "sk-SECRET" not in txt   # secrets still redacted
