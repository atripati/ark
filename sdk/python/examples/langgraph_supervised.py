"""Supervised LangGraph integration: proposed tool action -> ARK check -> REJECT/ALLOW.

A REAL LangGraph agent tries to book a flight. Its `book_flight` tool is wrapped with ARK
supervision against the runtime constraint "book the 2nd-cheapest". The agent proposes the
cheapest (wrong); ARK REJECTS and returns the rank-2 evidence; LangGraph's own loop feeds
that back and the agent re-proposes; ARK ALLOWS; the real tool runs. ARK never authors the
agent's action and never runs the agent — LangGraph does.

The retry budget, verdict, and recovery all come from Go ARK. The adapter only calls the
generic session primitives. Model turns are recorded for cost; the supervised tool records
its own decisions (so the handler uses record_tools=False to avoid double-counting).

Run:  PYTHONPATH=. ARK_BRIDGE_BIN=/path/to/ark-bridge python examples/langgraph_supervised.py
"""
import warnings
warnings.filterwarnings("ignore")

from typing import List, Optional

from langchain_core.callbacks.manager import CallbackManagerForLLMRun
from langchain_core.language_models.chat_models import BaseChatModel
from langchain_core.messages import AIMessage, BaseMessage
from langchain_core.outputs import ChatGeneration, ChatResult
from langchain_core.tools import tool

import ark
from ark.integrations.langgraph import ArkCallbackHandler, ark_supervise_tool, build_agent

# trusted runtime evidence the agent's own search produced (prices; rank 2 = "B").
EVIDENCE = {"requested_rank": 2, "evidence_complete": True,
            "options": [{"id": "A", "price": 163, "is_direct": True},
                        {"id": "B", "price": 290, "is_direct": True},
                        {"id": "C", "price": 410, "is_direct": True}]}


class ReplanModel(BaseChatModel):
    """Propose A (cheapest); after ARK's rejection feedback, propose B; then answer."""
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
        if self._turn == 1:
            opt = "A"
        elif "ark blocked" in last or "suggested: b" in last:
            opt = "B"
        else:
            opt = None
        if opt:
            msg = AIMessage(content="",
                            tool_calls=[{"name": "book_flight", "args": {"option": opt}, "id": f"c{self._turn}"}],
                            usage_metadata={"input_tokens": 400, "output_tokens": 16, "total_tokens": 416},
                            response_metadata={"model_name": "gpt-4o-mini"})
        else:
            msg = AIMessage(content="Booked option B, the 2nd-cheapest.",
                            usage_metadata={"input_tokens": 900, "output_tokens": 24, "total_tokens": 924},
                            response_metadata={"model_name": "gpt-4o"})
        return ChatResult(generations=[ChatGeneration(message=msg)])


@tool
def book_flight(option: str) -> str:
    """Book the given flight option id."""
    return f"BOOKED {option}"


def main():
    with ark.ARK(supervision="experimental").trace(task="book the 2nd-cheapest flight",
                                                   task_type="booking") as run:
        supervised_book = ark_supervise_tool(run, book_flight, constraint="rank", evidence=EVIDENCE)
        agent = build_agent(ReplanModel(), [supervised_book])
        # record_tools=False: the wrapped tool records its own (supervised) decisions
        agent.invoke({"messages": [("user", "book the 2nd cheapest flight")]},
                     config={"callbacks": [ArkCallbackHandler(run, record_tools=False)]})
    result = run.result

    print("success:", result.success, "| total_cost:", round(result.total_cost, 6))
    print("supervision:", result.supervision.by_verdict, "| interventions:", result.supervision.interventions)
    print("\nproposal -> supervision -> retry -> execution chain:")
    for d in result.decisions:
        s = d.supervision
        sv = (f" [{s.verdict} suggested={s.suggested_from_evidence} retry={s.retry_number} exec={d.executed}]"
              if s else "")
        print(f"  {d.id}  {d.action:<11} model={d.model} tool={d.tool} cost={d.cost.total_cost:.6f}{sv}")


if __name__ == "__main__":
    main()
