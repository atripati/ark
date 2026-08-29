"""LIVE supervised LangGraph + real OpenAI model + ARK pre-tool supervision.

Real lifecycle (both model turns are real OpenAI calls):
    model proposes action A -> ARK pre-tool check -> REJECT -> A does NOT execute ->
    supervision feedback goes back into LangGraph -> the model itself authors the next
    action -> model proposes B -> ARK ALLOW -> B executes -> final answer.

ARK enforces an independent policy ("book the rank-2 / 2nd-cheapest option") over whatever
the model proposes. ARK never authors the replacement action: on a non-ALLOW verdict it
returns only feedback text; the next concrete tool call is authored by the model.

    export OPENAI_API_KEY=sk-...            # your key; the LangGraph app owns it
    export ARK_BRIDGE_BIN=/path/to/ark-bridge
    PYTHONPATH=. python examples/langgraph_live_supervised.py
"""
import os
import warnings
warnings.filterwarnings("ignore")

from langchain_core.callbacks import BaseCallbackHandler
from langchain_core.messages import AIMessage, ToolMessage
from langchain_core.tools import tool
from langchain_openai import ChatOpenAI

import ark
from ark.integrations.langgraph import ArkCallbackHandler, ark_supervise_tool, build_agent

MODEL = os.environ.get("ARK_LIVE_MODEL", "gpt-4o-mini")

# ARK's trusted runtime evidence: three priced options; the enforced rank is 2 => "B".
EVIDENCE = {"requested_rank": 2, "evidence_complete": True,
            "options": [{"id": "A", "price": 163, "is_direct": True},
                        {"id": "B", "price": 290, "is_direct": True},
                        {"id": "C", "price": 410, "is_direct": True}]}

BOOKED = []


@tool
def book_flight(option: str) -> str:
    """Book the flight option with the given id (A, B or C)."""
    BOOKED.append(option)          # real side effect — proves whether A ever executed
    return f"BOOKED {option}"


class AuthorshipProbe(BaseCallbackHandler):
    """Captures the RAW tool calls the model authored on each turn, and the tool results
    (ARK feedback vs real bookings) — the evidence that the model, not ARK, authors actions."""
    def __init__(self):
        self.model_authored = []      # [(turn, [(name,args)])]
        self.tool_results = []        # [content]
        self._turn = 0

    def on_llm_end(self, response, *, run_id, **kw):
        self._turn += 1
        msg = response.generations[0][0].message
        if getattr(msg, "tool_calls", None):
            self.model_authored.append((self._turn, [(tc["name"], tc["args"]) for tc in msg.tool_calls]))

    def on_tool_end(self, output, *, run_id, **kw):
        self.tool_results.append(getattr(output, "content", str(output)))


def main():
    if not os.environ.get("OPENAI_API_KEY"):
        raise SystemExit("set OPENAI_API_KEY in your environment first (it is never printed)")

    model = ChatOpenAI(model=MODEL, temperature=0)   # the app owns the model call
    probe = AuthorshipProbe()

    with ark.ARK(supervision="experimental").trace(task="book a flight (ARK enforces rank-2 policy)",
                                                   task_type="booking", provider="openai") as run:
        supervised_book = ark_supervise_tool(run, book_flight, constraint="rank", evidence=EVIDENCE)
        agent = build_agent(model, [supervised_book])
        task = ("Book me the CHEAPEST available flight. Options: A costs $163, B costs $290, "
                "C costs $410. Call book_flight with the option id.")
        out = agent.invoke({"messages": [("user", task)]},
                           config={"callbacks": [ArkCallbackHandler(run, record_tools=False), probe]})
    result = run.result

    print("=" * 72)
    print("model:", MODEL, "| ARK policy: rank-2 (=> option B)")
    print("=" * 72)
    print("\nDECISION CHAIN (canonical RunResult):")
    for d in result.decisions:
        s = d.supervision
        sv = (f" [{s.verdict} suggested={s.suggested_from_evidence} retry={s.retry_number} exec={d.executed}]"
              if s else "")
        print(f"  {d.id}  {d.action:<11} model={d.model} tool={d.tool} "
              f"cost={d.cost.total_cost:.8f}{sv}")

    print("\nAUTHORSHIP EVIDENCE:")
    print("  model-authored tool calls (raw, from each real OpenAI turn):")
    for turn, calls in probe.model_authored:
        print(f"    turn {turn}: {calls}")
    print("  tool results fed back to the model:")
    for c in probe.tool_results:
        kind = "ARK feedback" if str(c).startswith("ARK blocked") else "real booking"
        print(f"    [{kind}] {c}")
    print("  real bookings that actually executed (tool side effects):", BOOKED)

    # the proofs the milestone asks for
    supervised = [d for d in result.decisions if d.supervision]
    verdicts = [d.supervision.verdict for d in supervised]
    print("\nPROOFS:")
    print("  verdict sequence:", verdicts)
    if "REJECT" in verdicts:
        rej = next(d for d in supervised if d.supervision.verdict == "REJECT")
        print("  rejected proposal executed =", rej.executed, "(must be False)")
    allw = next((d for d in supervised if d.supervision.verdict == "ALLOW"), None)
    if allw:
        print("  allowed proposal executed  =", allw.executed, "retry_number =", allw.supervision.retry_number,
              "(retry maintained by Go)")
    print("  'A' (cheapest, rejected) in real bookings:", "A" in BOOKED, "(must be False)")
    print("  final answer:", out["messages"][-1].content[:120])


if __name__ == "__main__":
    main()
