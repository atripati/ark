"""LIVE LangGraph + real OpenAI tests. Skipped unless OPENAI_API_KEY is set (and
langchain-openai installed). These make real, metered API calls with the developer's own key;
the key is read from the environment and never printed. Model: $ARK_LIVE_MODEL or gpt-4o-mini.

Run:  OPENAI_API_KEY=sk-... ARK_BRIDGE_BIN=/path/to/ark-bridge \
      python -m pytest tests/test_langgraph_live.py -q -s
"""
import os
import warnings

import pytest

pytest.importorskip("langgraph")
pytest.importorskip("langchain_openai")
if not os.environ.get("OPENAI_API_KEY"):
    pytest.skip("OPENAI_API_KEY not set — skipping live OpenAI tests", allow_module_level=True)
warnings.filterwarnings("ignore")

from langchain_core.callbacks import BaseCallbackHandler
from langchain_core.messages import AIMessage
from langchain_core.tools import tool
from langchain_openai import ChatOpenAI

import ark
from ark.integrations.langgraph import ArkCallbackHandler, ark_supervise_tool, build_agent

MODEL = os.environ.get("ARK_LIVE_MODEL", "gpt-4o-mini")
_BOOKED = []


@tool
def word_count(text: str) -> int:
    """Return the number of whitespace-separated words in text."""
    return len(text.split())


@tool
def book_flight(option: str) -> str:
    """Book the flight option id (A, B or C)."""
    _BOOKED.append(option)
    return f"BOOKED {option}"


class _Authorship(BaseCallbackHandler):
    def __init__(self):
        self.authored = []
    def on_llm_end(self, response, *, run_id, **kw):
        msg = response.generations[0][0].message
        if isinstance(msg, AIMessage) and msg.tool_calls:
            self.authored.append([tc["args"] for tc in msg.tool_calls])


def test_live_observe_real_model_real_tool():
    model = ChatOpenAI(model=MODEL, temperature=0)          # the app owns the model call
    agent = build_agent(model, [word_count])
    task = ("How many words are in: 'the quick brown fox jumps over the lazy dog'? "
            "Use the word_count tool.")
    with ark.trace(task=task, task_type="tool_use", provider="openai") as run:
        agent.invoke({"messages": [("user", task)]},
                     config={"callbacks": [ArkCallbackHandler(run)]})
    r = run.result
    assert r.success is True
    assert any(d.model for d in r.decisions)                # a real model turn was recorded
    assert any(d.tool == "word_count" for d in r.decisions)  # the tool actually executed
    assert r.total_tokens > 0 and r.total_cost > 0          # real tokens -> ARK-derived cost
    # every model turn carries real usage; cost reconciles across the derived aggregates
    model_cost = sum(d.cost.total_cost for d in r.decisions if d.model)
    assert abs(model_cost - r.total_cost) < 1e-12
    assert abs(sum(r.cost_by_model.values()) - r.total_cost) < 1e-12
    # provenance: tokens/model are REPORTED by LangGraph; cost is DERIVED by ARK
    pv = next(iter(run.provenance["decisions"].values()))
    assert "model" in pv["reported"] and "cost" in pv["derived"]
    # ARK observed only — no ark.run(mode="live"); provider reported, never "configured"
    assert r.providers.get("openai") == "reported"


def test_live_supervised_reject_then_model_replans_then_allow():
    _BOOKED.clear()
    probe = _Authorship()
    model = ChatOpenAI(model=MODEL, temperature=0)
    ev = {"requested_rank": 2, "evidence_complete": True,
          "options": [{"id": "A", "price": 163, "is_direct": True},
                      {"id": "B", "price": 290, "is_direct": True},
                      {"id": "C", "price": 410, "is_direct": True}]}
    with ark.ARK(supervision="experimental").trace(task="book (ARK rank-2 policy)",
                                                   task_type="booking", provider="openai") as run:
        supervised = ark_supervise_tool(run, book_flight, constraint="rank", evidence=ev)
        agent = build_agent(model, [supervised])
        task = ("Book me the CHEAPEST available flight. Options: A costs $163, B costs $290, "
                "C costs $410. Call book_flight with the option id.")
        agent.invoke({"messages": [("user", task)]},
                     config={"callbacks": [ArkCallbackHandler(run, record_tools=False), probe]})
    r = run.result
    sup = [d for d in r.decisions if d.supervision]
    verdicts = [d.supervision.verdict for d in sup]
    # the model proposed the cheapest (A); ARK's rank-2 policy rejected it, then allowed B
    assert verdicts[-1] == "ALLOW"
    assert "REJECT" in verdicts, f"expected a rejection then replan, got {verdicts}"
    rej = next(d for d in sup if d.supervision.verdict == "REJECT")
    allw = next(d for d in sup if d.supervision.verdict == "ALLOW")
    assert rej.executed is False and allw.executed is True          # rejected didn't run; allowed did
    assert allw.supervision.retry_number >= 1                        # retry state maintained by Go
    assert "A" not in _BOOKED                                        # the cheapest never executed
    assert _BOOKED and _BOOKED[-1] == "B"                            # the allowed option executed
    # AUTHORSHIP: each executed/attempted option id was authored by the model, not ARK
    all_authored = [args["option"] for turn in probe.authored for args in turn]
    assert all_authored[0] == "A" and "B" in all_authored           # model authored A then B
