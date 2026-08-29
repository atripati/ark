"""LangGraph adapter tests.

Two kinds of proof:
  1. delegation — with a SPY session, the adapter only calls the generic primitives
     (run.record / run.check) and computes no cost/verdict/routing of its own;
  2. end-to-end — a real LangGraph create_react_agent, driven through the real Go bridge,
     yields a canonical RunResult with the full chain and reconciled cost.

Skipped entirely if langgraph isn't installed, so the base SDK suite is unaffected.
"""
import math
import warnings

import pytest

pytest.importorskip("langgraph")
pytest.importorskip("langchain_core")
warnings.filterwarnings("ignore")

from typing import List, Optional

from langchain_core.callbacks.manager import CallbackManagerForLLMRun
from langchain_core.language_models.chat_models import BaseChatModel
from langchain_core.messages import AIMessage, BaseMessage
from langchain_core.outputs import ChatGeneration, ChatResult, LLMResult
from langchain_core.tools import tool
from langgraph.prebuilt import create_react_agent

import ark
from ark.integrations.langgraph import ArkCallbackHandler, ark_supervise_tool, _llm_end_fields


def approx(a, b):
    return math.isclose(a, b, abs_tol=1e-9)


# ---- spies: prove the adapter uses the generic session, not duplicate logic ----

class _FakeVerdict:
    def __init__(self, decision_id, verdict, suggested=None, reason="r"):
        self.decision_id, self.verdict = decision_id, verdict
        self.allowed = verdict == "ALLOW"
        self.suggested, self.reason = suggested, reason


class SpyRun:
    """Mimics ark.RunSession's primitive surface and records every call verbatim."""
    def __init__(self, verdicts=None):
        self.records = []
        self.checks = []
        self._verdicts = list(verdicts or [])

    def record(self, **kwargs):
        self.records.append(kwargs)
        return f"decision_{len(self.records):03d}"

    def check(self, *, proposed_action, constraint, evidence, action="tool_call", tool=None):
        self.checks.append(dict(proposed_action=proposed_action, constraint=constraint,
                                evidence=evidence, action=action, tool=tool))
        return self._verdicts.pop(0)


@tool
def book_flight(option: str) -> str:
    """Book a flight option."""
    return f"BOOKED {option}"


def test_handler_reports_tokens_and_model_but_derives_no_cost():
    spy = SpyRun()
    h = ArkCallbackHandler(spy)
    msg = AIMessage(content="hi", usage_metadata={"input_tokens": 100, "output_tokens": 20, "total_tokens": 120},
                    response_metadata={"model_name": "gpt-4o"})
    h.on_llm_end(LLMResult(generations=[[ChatGeneration(message=msg)]]), run_id="r1")
    assert len(spy.records) == 1
    rec = spy.records[0]
    assert rec["model"] == "gpt-4o" and rec["input_tokens"] == 100 and rec["output_tokens"] == 20
    # the adapter passes tokens+model and lets ARK derive cost — it never computes one itself
    assert rec.get("cost") is None


def test_handler_maps_tool_events_to_tool_decisions():
    spy = SpyRun()
    h = ArkCallbackHandler(spy)
    h.on_tool_start({"name": "book_flight"}, "{'option': 'A'}", run_id="t1", inputs={"option": "A"})
    h.on_tool_end("BOOKED A", run_id="t1")
    assert spy.records == [dict(action="tool_call", tool="book_flight", tool_args={"option": "A"},
                                latency_ms=spy.records[0]["latency_ms"], outcome="success")]
    assert spy.records[0]["model"] is None if "model" in spy.records[0] else True  # no model on tool decision


def test_handler_record_tools_false_skips_tool_events():
    spy = SpyRun()
    h = ArkCallbackHandler(spy, record_tools=False)
    h.on_tool_start({"name": "book_flight"}, "{}", run_id="t1", inputs={"option": "A"})
    h.on_tool_end("ok", run_id="t1")
    assert spy.records == []  # tools owned by the supervised wrapper instead


def test_supervise_wrapper_only_calls_check_and_record():
    spy = SpyRun(verdicts=[_FakeVerdict("decision_001", "REJECT", suggested="B"),
                           _FakeVerdict("decision_002", "ALLOW")])
    wrapped = ark_supervise_tool(spy, book_flight, constraint="rank",
                                 evidence={"requested_rank": 2})
    # 1st proposal (A) is rejected: the real tool must NOT run; ARK's suggestion is returned
    out_reject = wrapped.invoke({"option": "A"})
    assert "BOOKED" not in out_reject and "B" in out_reject
    # 2nd proposal (B) is allowed: the real tool runs
    out_allow = wrapped.invoke({"option": "B"})
    assert out_allow == "BOOKED B"
    # the wrapper touched only the generic primitives, mapping the verdict through faithfully
    assert [c["proposed_action"] for c in spy.checks] == [{"option": "A"}, {"option": "B"}]
    assert spy.records[0]["executed"] is False and spy.records[0]["of"].verdict == "REJECT"
    assert spy.records[1]["outcome"] == "success" and spy.records[1]["of"].verdict == "ALLOW"


def test_llm_end_fields_tolerates_legacy_llm_output():
    r = LLMResult(generations=[[ChatGeneration(message=AIMessage(content="x"))]],
                  llm_output={"model_name": "gpt-4o", "token_usage": {"prompt_tokens": 7, "completion_tokens": 3}})
    model, in_tok, out_tok, tools = _llm_end_fields(r)
    assert model == "gpt-4o" and in_tok == 7 and out_tok == 3 and tools == []


# ---- end-to-end: real agent + real Go bridge -> canonical RunResult ----

class _ScriptedModel(BaseChatModel):
    _turn: int = 0

    @property
    def _llm_type(self):
        return "scripted"

    def bind_tools(self, tools, **kwargs):
        return self

    def _generate(self, messages: List[BaseMessage], stop=None,
                  run_manager: Optional[CallbackManagerForLLMRun] = None, **kwargs) -> ChatResult:
        object.__setattr__(self, "_turn", self._turn + 1)
        if self._turn == 1:
            m = AIMessage(content="", tool_calls=[{"name": "book_flight", "args": {"option": "A"}, "id": "c1"}],
                          usage_metadata={"input_tokens": 449, "output_tokens": 16, "total_tokens": 465},
                          response_metadata={"model_name": "gpt-4o-mini"})
        else:
            m = AIMessage(content="done", usage_metadata={"input_tokens": 882, "output_tokens": 186, "total_tokens": 1068},
                          response_metadata={"model_name": "gpt-4o"})
        return ChatResult(generations=[ChatGeneration(message=m)])


def test_end_to_end_observe_runresult_and_cost_reconcile():
    agent = create_react_agent(_ScriptedModel(), [book_flight])
    with ark.trace(task="book a flight", task_type="booking") as run:
        agent.invoke({"messages": [("user", "book it")]},
                     config={"callbacks": [ArkCallbackHandler(run)]})
    r = run.result
    assert r.success is True
    # model turns carry cost; the tool execution is a separate zero-cost activity decision
    assert any(d.action == "tool_call" and d.tool == "book_flight" for d in r.decisions)
    assert r.cost_by_model.get("gpt-4o-mini") and r.cost_by_model.get("gpt-4o")
    model_cost = sum(d.cost.total_cost for d in r.decisions if d.model)
    assert approx(model_cost, r.total_cost)                 # cost reconciles
    assert approx(sum(r.cost_by_model.values()), r.total_cost)
    # gpt-4o-mini: 449*0.15/M + 16*0.60/M ; gpt-4o: 882*2.5/M + 186*10/M
    assert approx(r.total_cost, (449 * 0.15 + 16 * 0.60) / 1e6 + (882 * 2.5 + 186 * 10) / 1e6)


class _ReplanModel(BaseChatModel):
    _turn: int = 0

    @property
    def _llm_type(self):
        return "replan"

    def bind_tools(self, tools, **kwargs):
        return self

    def _generate(self, messages: List[BaseMessage], stop=None,
                  run_manager: Optional[CallbackManagerForLLMRun] = None, **kwargs) -> ChatResult:
        object.__setattr__(self, "_turn", self._turn + 1)
        last = str(getattr(messages[-1], "content", "")).lower()
        opt = "A" if self._turn == 1 else ("B" if ("ark blocked" in last or "suggested: b" in last) else None)
        if opt:
            m = AIMessage(content="", tool_calls=[{"name": "book_flight", "args": {"option": opt}, "id": f"c{self._turn}"}],
                          usage_metadata={"input_tokens": 400, "output_tokens": 16, "total_tokens": 416},
                          response_metadata={"model_name": "gpt-4o-mini"})
        else:
            m = AIMessage(content="booked B", usage_metadata={"input_tokens": 900, "output_tokens": 20, "total_tokens": 920},
                          response_metadata={"model_name": "gpt-4o"})
        return ChatResult(generations=[ChatGeneration(message=m)])


def test_end_to_end_supervised_full_chain():
    ev = {"requested_rank": 2, "evidence_complete": True,
          "options": [{"id": "A", "price": 163, "is_direct": True},
                      {"id": "B", "price": 290, "is_direct": True}]}
    with ark.ARK(supervision="experimental").trace(task="book 2nd-cheapest", task_type="booking") as run:
        supervised = ark_supervise_tool(run, book_flight, constraint="rank", evidence=ev)
        agent = create_react_agent(_ReplanModel(), [supervised])
        agent.invoke({"messages": [("user", "book the 2nd cheapest")]},
                     config={"callbacks": [ArkCallbackHandler(run, record_tools=False)]})
    r = run.result
    # the canonical RunResult contains proposal -> supervision -> retry -> execution -> cost
    supervised_decisions = [d for d in r.decisions if d.supervision]
    verdicts = [d.supervision.verdict for d in supervised_decisions]
    assert verdicts == ["REJECT", "ALLOW"]
    rej = supervised_decisions[0]
    assert rej.executed is False and rej.supervision.suggested_from_evidence == "B"
    assert approx(rej.cost.total_cost, 0.0)                 # rejected proposal not executed
    allw = supervised_decisions[1]
    assert allw.executed is True and allw.supervision.retry_number == 1
    assert r.supervision.enabled and r.supervision.by_verdict == {"REJECT": 1, "ALLOW": 1}
    # cost still comes only from model turns; supervision added no pricing of its own
    assert approx(sum(d.cost.total_cost for d in r.decisions if d.model), r.total_cost)
