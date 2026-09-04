"""External-agent `trace` session tests: an arbitrary Python loop attaches ARK, reports
decisions, optionally gates actions, and receives the canonical RunResult with the full
proposal -> supervision -> retry -> execution -> outcome -> cost chain.
"""
import io
import json as _json
import math
import queue
import subprocess
import threading
import time

import pytest

import ark
from ark import ARK, ArkBridgeError, ArkSupervisionDisabled, RunResult
from ark.session import Verdict, _SessionProc


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
        v1 = run.check(proposed_action={"option": "A"}, constraint="rank", evidence=ev, scope="order-1", tool="book")
        assert isinstance(v1, Verdict)
        assert v1.verdict == "REJECT" and v1.allowed is False
        assert v1.suggested == "B" and v1.retry_number == 0

        v2 = run.check(proposed_action={"option": "B"}, constraint="rank", evidence=ev, scope="order-1", tool="book")
        assert v2.verdict == "ALLOW" and v2.allowed is True
        assert v2.retry_number == 1                      # retry state advanced IN GO
        run.record(action="tool_call", tool="book", model="gpt-4o",
                   input_tokens=882, output_tokens=186, of=v2,
                   executed_action={"option": "B"})     # MANDATORY: the actual executed action

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
            v = run.check(proposed_action={"option": "A"}, constraint="rank", evidence=ev, scope="order-1", tool="book")
            verdicts.append(v.verdict)
            if v.verdict == "RECOVERY_EXHAUSTED":
                break
    # budget=3 -> three REJECTs (retry 0,1,2) then RECOVERY_EXHAUSTED at retry 3
    assert verdicts == ["REJECT", "REJECT", "REJECT", "RECOVERY_EXHAUSTED"]


def test_of_verdict_links_execution_to_proposal():
    ev = {"requested_rank": 2, "evidence_complete": True,
          "options": [{"id": "A", "price": 163}, {"id": "B", "price": 290}]}
    with ARK(supervision="experimental").trace(task="book") as run:
        v = run.check(proposed_action={"option": "B"}, constraint="rank", evidence=ev, scope="order-1", tool="book")
        did = run.record(action="tool_call", tool="book", model="gpt-4o",
                         input_tokens=100, output_tokens=20, of=v,
                         executed_action={"option": "B"})
    assert did == v.decision_id                         # same decision, unambiguous chain
    assert len(run.result.decisions) == 1


# ---- guardrails -----------------------------------------------------------------

def test_check_requires_experimental_supervision():
    with ark.trace(task="x") as run:  # supervision defaults off
        with pytest.raises(ArkSupervisionDisabled):
            run.check(proposed_action={"option": "A"}, constraint="rank", evidence={}, scope="order-1")


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


# ---- transport concurrency (regression for the LangGraph ToolNode thread-pool corruption) --

def test_sessionproc_serializes_concurrent_sends_unit():
    # deterministic, no real bridge (uses _FakeBridge/_attach defined below): many threads hit
    # one _SessionProc at once. The reader feeds a FIFO queue and the lock makes each send take
    # exactly its own response, so every request gets ITS OWN id back — a crossed response would
    # mean the serialization broke.
    proc = _attach(_FakeBridge(hang=False), timeout=5)
    results, errors = {}, []

    def worker(i):
        try:
            results[i] = proc.send({"cmd": "record", "id": f"req-{i}"})["decision_id"]
        except Exception as e:
            errors.append(e)

    threads = [threading.Thread(target=worker, args=(i,)) for i in range(24)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    try:
        assert not errors, errors[:3]
        assert all(results[i] == f"req-{i}" for i in range(24))  # each got ITS OWN response
    finally:
        proc.close()


def test_concurrent_records_on_real_bridge_session():
    # real ark-bridge --session hit from many threads: the exact pattern that corrupted the
    # pipe under LangGraph. After the fix: no errors, unique/valid ids, finish() works.
    n_threads, per = 8, 8
    errors = []
    with ark.trace("concurrency stress") as run:
        def worker():
            for _ in range(per):
                try:
                    run.record(action="tool_call", tool="t", model="gpt-4o-mini",
                               input_tokens=10, output_tokens=2, outcome="success")
                except Exception as e:
                    errors.append(e)

        threads = [threading.Thread(target=worker) for _ in range(n_threads)]
        for t in threads:
            t.start()
        for t in threads:
            t.join()
        assert not errors, errors[:3]
    r = run.result
    total = n_threads * per
    ids = [d.id for d in r.decisions]
    assert len(ids) == total
    assert len(set(ids)) == total                                       # unique
    assert sorted(ids) == [f"decision_{i:03d}" for i in range(1, total + 1)]  # valid/sequential
    assert r.success is True
    assert all(d.model == "gpt-4o-mini" for d in r.decisions)


def test_concurrent_checks_and_records_experimental_bridge():
    ev = {"requested_rank": 2, "evidence_complete": True,
          "options": [{"id": "A", "price": 100}, {"id": "B", "price": 200}]}
    errors = []
    with ARK(supervision="experimental").trace("mixed concurrency") as run:
        def check_worker():
            try:
                v = run.check(proposed_action={"option": "B"}, constraint="rank",
                              evidence=ev, scope="order-1", tool="book")
                if v.verdict != "ALLOW":
                    errors.append(f"unexpected verdict {v.verdict}")
            except Exception as e:
                errors.append(e)

        def record_worker():
            try:
                run.record(action="complete", model="gpt-4o", input_tokens=5, output_tokens=1)
            except Exception as e:
                errors.append(e)

        threads = [threading.Thread(target=check_worker if i % 2 else record_worker)
                   for i in range(12)]
        for t in threads:
            t.start()
        for t in threads:
            t.join()
        assert not errors, errors[:3]
    r = run.result
    assert len(r.decisions) == 12
    assert len({d.id for d in r.decisions}) == 12
    assert r.success is True


# ---- transport timeout (regression: a hung bridge must not wedge callers) -------------------

class _FakeBridge:
    """A controllable stand-in for `ark-bridge --session`. In hang mode it never produces a
    response, so the reader's readline blocks until the process is killed (then returns EOF)."""

    def __init__(self, hang=False):
        self._hang = hang
        self._out = queue.Queue()
        self._killed = threading.Event()
        self._alive = True
        self.kill_count = 0
        self.stdin = _FakeBridge._In(self)
        self.stdout = _FakeBridge._Out(self)
        self.stderr = io.BytesIO(b"")

    def poll(self):
        return None if self._alive else -9

    def kill(self):
        self.kill_count += 1
        self._alive = False
        self._killed.set()

    def wait(self, timeout=None):
        if not self._killed.wait(timeout):
            raise subprocess.TimeoutExpired("bridge", timeout)
        return -9

    class _In:
        def __init__(self, p):
            self.p = p
            self.closed = False

        def write(self, payload):
            if self.p._hang:
                return
            cmd = _json.loads(payload.decode())
            self.p._out.put((_json.dumps({"ok": True, "decision_id": cmd.get("id", "d")}) + "\n").encode())

        def flush(self):
            pass

        def close(self):
            self.closed = True
            self.p._killed.set()

    class _Out:
        def __init__(self, p):
            self.p = p

        def readline(self):
            while True:
                try:
                    return self.p._out.get(timeout=0.02)
                except queue.Empty:
                    if self.p._killed.is_set():
                        return b""


def _attach(fake, timeout):
    proc = _SessionProc(binary="unused", timeout=timeout)
    proc._p = fake
    proc._responses = queue.Queue()
    proc._dead = False
    proc._reader = threading.Thread(target=proc._read_loop, args=(fake,), daemon=True)
    proc._reader.start()
    return proc


def test_send_normal_response_via_reader():
    proc = _attach(_FakeBridge(hang=False), timeout=2)
    try:
        assert proc.send({"cmd": "record", "id": "abc"})["decision_id"] == "abc"
        assert proc.send({"cmd": "record", "id": "def"})["decision_id"] == "def"  # serial reuse
    finally:
        proc.close()


def test_send_times_out_kills_process_and_marks_unusable():
    fake = _FakeBridge(hang=True)
    proc = _attach(fake, timeout=0.3)
    try:
        t0 = time.monotonic()
        with pytest.raises(ArkBridgeError) as ei:
            proc.send({"cmd": "record", "id": "x"})
        dt = time.monotonic() - t0
        assert 0.25 <= dt < 3.0, dt                    # near the 0.3s timeout, not 120s or instant
        assert "timed out" in str(ei.value)
        assert fake.kill_count >= 1                     # the hung process was terminated
        # a subsequent send fails cleanly instead of consuming a stale/late response
        with pytest.raises(ArkBridgeError):
            proc.send({"cmd": "record", "id": "y"})
    finally:
        proc.close()                                    # close() is safe after a timeout


def test_close_is_safe_after_timeout():
    fake = _FakeBridge(hang=True)
    proc = _attach(fake, timeout=0.2)
    with pytest.raises(ArkBridgeError):
        proc.send({"cmd": "record", "id": "x"})
    proc.close()          # must not raise
    proc.close()          # idempotent


def test_concurrent_callers_do_not_hang_when_first_request_hangs():
    fake = _FakeBridge(hang=True)
    proc = _attach(fake, timeout=0.3)
    results = []

    def worker():
        try:
            proc.send({"cmd": "record", "id": "z"})
            results.append("ok")
        except ArkBridgeError:
            results.append("err")

    threads = [threading.Thread(target=worker) for _ in range(4)]
    t0 = time.monotonic()
    for t in threads:
        t.start()
    for t in threads:
        t.join(timeout=5)
    dt = time.monotonic() - t0
    try:
        assert all(not t.is_alive() for t in threads)   # nobody stuck forever
        assert dt < 4.0, dt                             # bounded: one timeout, the rest fail fast
        assert results.count("err") == 4                # all raised cleanly, none got a response
    finally:
        proc.close()
