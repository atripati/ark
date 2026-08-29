"""Observe-only LangGraph integration.

A REAL LangGraph agent (create_react_agent) executes normally — its own model, its own tool,
its own loop. We attach ARK with one callback handler; ARK records the decisions, attributes
cost from the reported tokens, and captures tool activity. ARK does not run or alter the agent.

The chat model here is a scripted fake so the example needs no API key; drop in a real
ChatOpenAI/ChatAnthropic and nothing about the ARK wiring changes.

Run:  PYTHONPATH=. ARK_BRIDGE_BIN=/path/to/ark-bridge python examples/langgraph_observe.py
"""
import warnings
warnings.filterwarnings("ignore")  # silence the create_react_agent v1.0 move notice

from typing import List, Optional

from langchain_core.callbacks.manager import CallbackManagerForLLMRun
from langchain_core.language_models.chat_models import BaseChatModel
from langchain_core.messages import AIMessage, BaseMessage
from langchain_core.outputs import ChatGeneration, ChatResult
from langchain_core.tools import tool

import ark
from ark.integrations.langgraph import ArkCallbackHandler, build_agent


class ScriptedChatModel(BaseChatModel):
    """Stand-in for your real chat model: search first, then answer. Both report usage."""
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
            msg = AIMessage(
                content="",
                tool_calls=[{"name": "search_frameworks", "args": {"query": "python web"}, "id": "c1"}],
                usage_metadata={"input_tokens": 449, "output_tokens": 16, "total_tokens": 465},
                response_metadata={"model_name": "gpt-4o-mini"})
        else:
            msg = AIMessage(
                content="Top Python web frameworks: 1. Django  2. Flask  3. FastAPI",
                usage_metadata={"input_tokens": 882, "output_tokens": 186, "total_tokens": 1068},
                response_metadata={"model_name": "gpt-4o"})
        return ChatResult(generations=[ChatGeneration(message=msg)])


@tool
def search_frameworks(query: str) -> str:
    """Search for popular frameworks."""
    return "django, flask, fastapi"


def main():
    agent = build_agent(ScriptedChatModel(), [search_frameworks])
    with ark.trace(task="find the top Python web frameworks", task_type="ranking") as run:
        agent.invoke({"messages": [("user", "top python web frameworks?")]},
                     config={"callbacks": [ArkCallbackHandler(run)]})
    result = run.result

    print("run_id:      ", result.run_id, "| success:", result.success)
    print("total_cost:  ", round(result.total_cost, 6), "(ARK-derived from reported tokens+model)")
    print("total_tokens:", result.total_tokens)
    print("by_model:    ", result.cost_by_model)
    print("tools:       ", result.tools.by_tool)
    print("decisions:")
    for d in result.decisions:
        print(f"  {d.id}  {d.action:<11} model={d.model} tool={d.tool} "
              f"cost={d.cost.total_cost:.6f} outcome={d.outcome}")

    # cost reconciliation: model turns carry cost; tool executions carry activity, no tokens
    model_cost = sum(d.cost.total_cost for d in result.decisions if d.model)
    assert abs(model_cost - result.total_cost) < 1e-9
    print("\ncost reconciles:", round(model_cost, 6), "==", round(result.total_cost, 6))


if __name__ == "__main__":
    main()
