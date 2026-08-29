"""Offline (no API key) proofs for the live-verification milestone:

  - AUTHORSHIP: on a non-ALLOW verdict ARK returns only feedback; the next concrete tool
    call is authored by the model, and ARK never constructs/executes the replacement — proven
    by a model that deliberately IGNORES ARK's suggestion and still drives the outcome.
  - PROVIDER INDEPENDENCE: removing ARK leaves the LangGraph agent structurally runnable.
  - ERROR VISIBILITY: a failing tool/model is represented in the canonical trace (not an
    unexplained silent success).

These use a scripted fake model so they run deterministically in CI; the LIVE equivalents
(real OpenAI) live in test_langgraph_live.py and run only when OPENAI_API_KEY is set.
"""
import warnings
from typing import List, Optional

import pytest

pytest.importorskip("langgraph")
warnings.filterwarnings("ignore")

from langchain_core.callbacks import BaseCallbackHandler
from langchain_core.callbacks.manager import CallbackManagerForLLMRun
from langchain_core.language_models.chat_models import BaseChatModel
from langchain_core.messages import AIMessage, BaseMessage
from langchain_core.outputs import ChatGeneration, ChatResult, LLMResult
from langchain_core.tools import tool
from langgraph.prebuilt import create_react_agent

import ark
from ark.integrations.langgraph import ArkCallbackHandler, ark_supervise_tool

BOOKED: List[str] = []


@tool
def book_flight(option: str) -> str:
    """Book a flight option id."""
    BOOKED.append(option)
    return f"BOOKED {option}"


class _ScriptModel(BaseChatModel):
    """Emits a fixed sequence of option ids, then a final answer. Independent of ARK."""
    def __init__(self, options, **kw):
        super().__init__(**kw)
        object.__setattr__(self, "_opts", list(options))
        object.__setattr__(self, "_turn", 0)

    @property
    def _llm_type(self):
        return "script"

    def bind_tools(self, tools, **kw):
        return self

    def _generate(self, messages: List[BaseMessage], stop=None,
                  run_manager: Optional[CallbackManagerForLLMRun] = None, **kw) -> ChatResult:
        i = self._turn
        object.__setattr__(self, "_turn", i + 1)
        if i < len(self._opts):
            m = AIMessage(content="", tool_calls=[{"name": "book_flight", "args": {"option": self._opts[i]}, "id": f"c{i}"}],
                          usage_metadata={"input_tokens": 100, "output_tokens": 8, "total_tokens": 108},
                          response_metadata={"model_name": "gpt-4o-mini"})
        else:
            m = AIMessage(content="done", usage_metadata={"input_tokens": 120, "output_tokens": 6, "total_tokens": 126},
                          response_metadata={"model_name": "gpt-4o-mini"})
        return ChatResult(generations=[ChatGeneration(message=m)])


class _Probe(BaseCallbackHandler):
    def __init__(self):
        self.authored = []
    def on_llm_end(self, response, *, run_id, **kw):
        msg = response.generations[0][0].message
        if getattr(msg, "tool_calls", None):
            self.authored.append([tc["args"]["option"] for tc in msg.tool_calls])


EV = {"requested_rank": 2, "evidence_complete": True,
      "options": [{"id": "A", "price": 163, "is_direct": True},
                  {"id": "B", "price": 290, "is_direct": True},
                  {"id": "C", "price": 410, "is_direct": True}]}


def test_model_authors_recovery_ark_never_forces_its_suggestion():
    """The model proposes A (rank1, REJECT->suggested B), then deliberately proposes C (rank3,
    REJECT->suggested B) IGNORING ARK's suggestion, then B (rank2, ALLOW). Proof: only B ever
    executes; ARK's 'suggested=B' was feedback, and the executed ids trace to the model."""
    BOOKED.clear()
    probe = _Probe()
    with ark.ARK(supervision="experimental").trace(task="book", provider="openai", budget=5) as run:
        supervised = ark_supervise_tool(run, book_flight, constraint="rank", evidence=EV)
        agent = create_react_agent(_ScriptModel(["A", "C", "B"]), [supervised])
        agent.invoke({"messages": [("user", "book one")]},
                     config={"callbacks": [ArkCallbackHandler(run, record_tools=False), probe]})
    r = run.result
    sup = [d for d in r.decisions if d.supervision]
    verdicts = [d.supervision.verdict for d in sup]
    # every rejection suggested B, yet the model authored C at turn 2 anyway
    assert verdicts == ["REJECT", "REJECT", "ALLOW"]
    assert all(d.supervision.suggested_from_evidence == "B" for d in sup if d.supervision.verdict == "REJECT")
    assert probe.authored == [["A"], ["C"], ["B"]]          # model authored each, incl. ignoring B
    # ARK never executed a replacement it authored: ONLY the model's allowed id ran
    assert BOOKED == ["B"]
    assert sup[0].executed is False and sup[2].executed is True


def test_rejected_proposal_returns_feedback_string_not_execution():
    """Direct proof at the wrapper: on REJECT the real tool is not called and the return value
    is ARK feedback text (which becomes a ToolMessage the model reads), not an executed action."""
    BOOKED.clear()
    class Spy:
        def check(self, *, proposed_action, constraint, evidence, action, tool):
            class V: decision_id="d1"; verdict="REJECT"; allowed=False; reason="rank-2 required"; suggested="B"
            return V()
        def record(self, **kw): return "d1"
    wrapped = ark_supervise_tool(Spy(), book_flight, constraint="rank", evidence=EV)
    out = wrapped.invoke({"option": "A"})
    assert isinstance(out, str) and "ARK blocked" in out and "Suggested: B" in out
    assert BOOKED == []                                     # the real tool never ran


def test_removing_ark_leaves_agent_runnable():
    """Provider independence: the SAME agent with NO ARK (no callbacks, no wrapper) still runs."""
    BOOKED.clear()
    agent = create_react_agent(_ScriptModel(["B"]), [book_flight])   # raw tool, no ARK
    out = agent.invoke({"messages": [("user", "book B")]})           # no ARK callbacks
    assert out["messages"][-1].content == "done"
    assert BOOKED == ["B"]                                           # ran fine without ARK


def test_tool_failure_is_represented_in_trace():
    """A tool that raises surfaces in the canonical trace via outcome='error: ...' (not a
    silent success). NOTE: the dedicated DecisionRecord.error field stays empty because the
    frozen record() API carries no error param — reported, not redesigned."""
    spy_records = []
    class Spy:
        def record(self, **kw): spy_records.append(kw); return "d"
    h = ArkCallbackHandler(Spy())
    h.on_tool_start({"name": "book_flight"}, "{}", run_id="t1", inputs={"option": "A"})
    h.on_tool_error(RuntimeError("db down"), run_id="t1")
    assert spy_records and spy_records[0]["outcome"].startswith("error:")
    assert "RuntimeError" in spy_records[0]["outcome"]


def test_model_error_is_represented_in_trace():
    spy_records = []
    class Spy:
        def record(self, **kw): spy_records.append(kw); return "d"
    h = ArkCallbackHandler(Spy())
    h.on_chat_model_start({"name": "ChatOpenAI"}, [[]], run_id="m1")
    h.on_llm_error(TimeoutError("upstream timeout"), run_id="m1")
    assert spy_records[0]["outcome"].startswith("error:") and spy_records[0]["executed"] is False
