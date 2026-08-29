"""LIVE observe-only LangGraph + real OpenAI model + real tool + ARK observation.

Execution path (ARK never makes the model call):
    your LangGraph agent -> your OpenAI model -> model proposes tool call ->
    LangGraph executes the tool -> ARK adapter observes callbacks ->
    ark.trace()/record() -> Go telemetry/cost -> canonical RunResult

Requires a real key IN THE ENVIRONMENT (never printed, never committed):
    export OPENAI_API_KEY=sk-...            # your key; the LangGraph app owns it
    export ARK_BRIDGE_BIN=/path/to/ark-bridge
    ARK_LIVE_MODEL defaults to gpt-4o-mini. Run:
    PYTHONPATH=. python examples/langgraph_live_observe.py
"""
import os
import warnings
warnings.filterwarnings("ignore")

from langchain_core.messages import AIMessage, ToolMessage
from langchain_core.tools import tool
from langchain_openai import ChatOpenAI

import ark
from ark.integrations.langgraph import ArkCallbackHandler, build_agent

MODEL = os.environ.get("ARK_LIVE_MODEL", "gpt-4o-mini")


@tool
def word_count(text: str) -> int:
    """Return the number of whitespace-separated words in text."""
    return len(text.split())


def main():
    if not os.environ.get("OPENAI_API_KEY"):
        raise SystemExit("set OPENAI_API_KEY in your environment first (it is never printed)")

    # The LangGraph APPLICATION owns the OpenAI client/key/model call — not ARK.
    model = ChatOpenAI(model=MODEL, temperature=0)   # reads OPENAI_API_KEY from env itself
    agent = build_agent(model, [word_count])

    task = ("How many words are in this sentence: "
            "'the quick brown fox jumps over the lazy dog'? Use the word_count tool.")

    with ark.trace(task=task, task_type="tool_use", provider="openai") as run:
        out = agent.invoke({"messages": [("user", task)]},
                           config={"callbacks": [ArkCallbackHandler(run)]})
    result = run.result

    print("=" * 72)
    print("1. exact live task:", task)
    print("2. model requested:", MODEL, "| model object owned by app:", type(model).__name__)
    print("3. tool used:      word_count (local python)")
    print("=" * 72)
    print("\n4. decision chain (canonical RunResult):")
    for d in result.decisions:
        print(f"   {d.id}  {d.action:<11} model={d.model} tool={d.tool} "
              f"in={d.cost.input_tokens} out={d.cost.output_tokens} "
              f"cost={d.cost.total_cost:.8f} latency_ms={d.latency_ms} outcome={d.outcome}")
    print("\n5. resolved model name(s) reported by OpenAI:",
          sorted({d.model for d in result.decisions if d.model}))
    print("6. total tokens:", result.total_tokens, "| total cost:", result.total_cost)
    print("7. cost_by_model:", result.cost_by_model)
    print("8. providers (ARK did not call them):", result.providers)

    # proposed vs executed tool calls, straight from the LangGraph message log
    print("\n9. proposed tool calls (authored by the model) vs executed results:")
    for m in out["messages"]:
        if isinstance(m, AIMessage) and m.tool_calls:
            print("   proposed by model:", [(tc["name"], tc["args"]) for tc in m.tool_calls])
        if isinstance(m, ToolMessage):
            print("   executed result:  ", m.name, "->", m.content)

    # cost reconciliation (model turns carry cost; tool executions carry activity)
    model_cost = sum(d.cost.total_cost for d in result.decisions if d.model)
    assert abs(model_cost - result.total_cost) < 1e-12, "cost must reconcile"
    assert abs(sum(result.cost_by_model.values()) - result.total_cost) < 1e-12
    print("\n10. cost reconciles:", round(model_cost, 8), "== total", round(result.total_cost, 8))

    print("\n11. provenance (LangGraph-reported vs ARK-derived):")
    for did, pv in sorted(run.provenance["decisions"].items()):
        print(f"   {did}: reported={pv['reported']} derived={pv['derived']}")

    print("\n12. PROOF ARK did not make the model call:")
    print("    - the model is a ChatOpenAI created by THIS app; ARK received only a callback")
    print("    - ark.trace()/ArkCallbackHandler hold no LLM client and issue no model requests")
    print("    - no ark.run(mode='live') was used; ARK only observed the external runtime")


if __name__ == "__main__":
    main()
