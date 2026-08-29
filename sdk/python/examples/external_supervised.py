"""Live supervised external-agent integration: proposal -> reject -> re-propose -> allow.

The external agent keeps control of execution. Before it books a flight it asks ARK to
check the proposed option against a runtime constraint ("book the 2nd-cheapest"). ARK
REJECTS the wrong option and returns runtime-derived evidence (the rank-2 id); the agent
re-authors its own next action (ARK never authors it), ARK ALLOWS it, the agent executes,
and the whole chain lands in one canonical RunResult.

The retry budget, verdict semantics and recovery logic all live in Go — this loop never
reimplements them.

Run:  python examples/external_supervised.py
"""
import ark

# trusted runtime evidence the agent's own search produced (prices, directness).
EVIDENCE = {
    "requested_rank": 2, "evidence_complete": True,
    "options": [
        {"id": "A", "price": 163, "is_direct": True},   # rank 1 (cheapest)
        {"id": "B", "price": 290, "is_direct": True},   # rank 2  <- required
        {"id": "C", "price": 410, "is_direct": True},
    ],
}


def my_agent_first_choice():
    return "A"          # the agent wrongly proposes the cheapest


def my_agent_repropose(suggested):
    # the agent re-plans using ARK's evidence — it decides, ARK does not author this.
    return suggested


def main():
    ark_client = ark.ARK(supervision="experimental")
    with ark_client.trace(task="book the 2nd-cheapest flight", task_type="booking") as run:
        # the agent searched first (unsupervised, cheap model)
        run.record(action="tool_call", tool="search_flights", model="gpt-4o-mini",
                   input_tokens=449, output_tokens=16, latency_ms=600,
                   routing_reason="cheap model for retrieval", outcome="success")

        proposed = my_agent_first_choice()
        for attempt in range(5):                        # your loop; ARK bounds it via the verdict
            v = run.check(proposed_action={"option": proposed}, constraint="rank",
                          evidence=EVIDENCE, action="tool_call", tool="book")
            print(f"attempt {attempt}: proposed={proposed} -> {v.verdict}"
                  + (f" (suggested={v.suggested})" if v.suggested else ""))
            if v.allowed:
                # your framework executes the booking; then you report it on THIS decision
                run.record(action="tool_call", tool="book", model="gpt-4o",
                           input_tokens=882, output_tokens=186, latency_ms=1400,
                           outcome="success", of=v)
                break
            if v.verdict == "RECOVERY_EXHAUSTED":
                print("recovery budget spent — not executing")
                break
            proposed = my_agent_repropose(v.suggested)  # agent re-authors the next action

    result = run.result
    print("\n=== canonical RunResult ===")
    print("success:", result.success, "| total_cost:", round(result.total_cost, 6))
    print("supervision:", result.supervision.by_verdict, "| interventions:", result.supervision.interventions)
    print("chain:")
    for d in result.decisions:
        s = d.supervision
        v = f" verdict={s.verdict} suggested={s.suggested_from_evidence} retry={s.retry_number}" if s else ""
        print(f"  {d.id} {d.action:<9} tool={d.tool} model={d.model} exec={d.executed} "
              f"cost={d.cost.total_cost:.6f}{v}")


if __name__ == "__main__":
    main()
