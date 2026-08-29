"""Task-3 error propagation: real external-runtime failures must populate the dedicated
canonical DecisionRecord.error field and aggregate into RunResult.errors — not merely appear
as outcome="error: ...". outcome is preserved for compatibility. A successful run gains no
false errors. Covers model/provider failure, tool failure, supervision/recovery failure, and
a successful run. Uses the real session + Go bridge (built by conftest)."""
import warnings

import pytest

pytest.importorskip("langgraph")
warnings.filterwarnings("ignore")

from langchain_core.tools import tool

import ark
from ark.integrations.langgraph import ArkCallbackHandler, ark_supervise_tool, _err


def test_model_failure_populates_error_field_and_errors_aggregate():
    with ark.trace(task="x") as run:
        h = ArkCallbackHandler(run)
        h.on_chat_model_start({"name": "ChatOpenAI"}, [[]], run_id="m1")
        h.on_llm_error(TimeoutError("upstream timeout"), run_id="m1")
    r = run.result
    d = r.decisions[0]
    assert d.error and "TimeoutError" in d.error          # dedicated field populated
    assert d.outcome and d.outcome.startswith("error:")    # outcome preserved for compat
    assert d.executed is False
    assert r.errors and any("TimeoutError" in e for e in r.errors)   # aggregated


def test_tool_failure_populates_error_field():
    with ark.trace(task="x") as run:
        h = ArkCallbackHandler(run)
        h.on_tool_start({"name": "book_flight"}, "{'option':'A'}", run_id="t1", inputs={"option": "A"})
        h.on_tool_error(RuntimeError("db connection refused"), run_id="t1")
    r = run.result
    d = r.decisions[0]
    assert d.tool == "book_flight" and d.error and "RuntimeError" in d.error
    assert r.errors and any("RuntimeError" in e for e in r.errors)


def test_supervised_tool_failure_populates_error_field():
    @tool
    def book_flight(option: str) -> str:
        """Book an option."""
        raise ValueError("booking backend exploded")

    ev = {"requested_rank": 2, "evidence_complete": True,
          "options": [{"id": "A", "price": 163}, {"id": "B", "price": 290}]}
    with ark.ARK(supervision="experimental").trace(task="book") as run:
        wrapped = ark_supervise_tool(run, book_flight, constraint="rank", evidence=ev)
        with pytest.raises(ValueError):        # allowed, then the real tool fails -> re-raised
            wrapped.invoke({"option": "B"})
    r = run.result
    failed = [d for d in r.decisions if d.error]
    assert failed and "ValueError" in failed[0].error
    assert failed[0].supervision and failed[0].supervision.verdict == "ALLOW"  # it was allowed
    assert failed[0].executed is True                                          # it attempted
    assert r.errors and any("ValueError" in e for e in r.errors)


def test_successful_run_has_no_false_errors():
    with ark.trace(task="x") as run:
        run.record(action="tool_call", tool="search", model="gpt-4o-mini",
                   input_tokens=10, output_tokens=5, outcome="success")
        run.record(action="complete", model="gpt-4o", input_tokens=20, output_tokens=8, outcome="stop")
    r = run.result
    assert all(d.error is None for d in r.decisions)
    assert not r.errors            # None or empty — never fabricated


def test_error_string_is_secret_scrubbed():
    # a provider error that happens to echo a key must never leak it into the trace
    scrubbed = _err(RuntimeError("401 unauthorized for key sk-proj-ABCDEF1234567890 on api_key=zzz"))
    assert "sk-proj-ABCDEF" not in scrubbed and "zzz" not in scrubbed
    assert "[redacted]" in scrubbed and scrubbed.startswith("RuntimeError")
