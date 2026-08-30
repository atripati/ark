# ARK

Runtime supervision and observability for AI agents.

Keep your model.
Keep your agent.
Keep your tools.
Add ARK around the runtime.

ARK sits around the decisions an agent makes. It records what happened and what it cost. With experimental supervision turned on, it can also check a proposed tool action before that action runs.

```bash
pip install ark-agent-runtime
```

```python
from ark import ARK
```

This is alpha software, version 0.1.0a1.

## Why ARK

An agent can know a rule and still propose the wrong tool call. Once that call runs, explaining it correctly afterward does not undo the side effect. The booking already happened. The row was already written.

ARK sits at that point, just before the call runs. It gives the runtime a place to look at the proposed action, the rule that applies, and the trusted evidence, and then decide whether the call goes through.

```
agent proposes an action
        |
        v
     ARK check
      |     \
   ALLOW     not ALLOW
      |          \
   it runs     feedback goes back, the agent proposes again
```

## Install

```bash
pip install ark-agent-runtime
```

The wheel ships with the Go runtime bridge for your platform. You do not need Go installed, the source repo, `PYTHONPATH`, or `ARK_BRIDGE_BIN`. The import stays `from ark import ARK`.

## Two ways to use ARK

### 1. Run a task through ARK

```python
from ark import ARK

ark = ARK()
result = ark.run("hello from ARK", mode="mock")

print(result.success, result.total_cost)
for d in result.decisions:
    print(d.id, d.action, d.model, d.cost.total_cost)
```

Mock mode works with no API key, so you can try it right away. `mode="live"` runs a real provider and needs a configured model, meaning an API key and an `agent.yaml`.

### 2. Put ARK around your own agent

You do not replace your agent to use ARK. You keep your framework, your model, and your tools, and you report what the agent did. ARK builds the same result.

```python
from ark import ARK

ark = ARK()
with ark.trace("book the second cheapest flight") as run:
    # your loop, your model, your tools
    run.record(action="tool_call", tool="search", model="gpt-4o-mini",
               input_tokens=449, output_tokens=16, outcome="success")

result = run.result
print(result.total_cost, [d.model for d in result.decisions])
```

This is the mode that matters if you already have an agent. ARK never runs your model. You do.

## Observability

Every run returns a `RunResult` with a list of decisions. A decision can carry:

- model and provider
- input and output tokens
- cost, which ARK works out from the tokens and model when you do not pass a cost yourself
- the tool or proposed action, and its outcome
- verification, when your runtime reports it
- a supervision verdict, when supervision is on
- a stable id, so a proposed action and the action that actually ran can point to the same decision
- latency, when it is available

Run totals include total cost, total tokens, and cost grouped by model, by tool, and by action.

Not every provider returns every field. Fields that are missing stay empty. ARK does not fill them in. `run.provenance` tells you, per decision, which facts your runtime reported and which ARK worked out.

## Runtime supervision (experimental)

Supervision is experimental and off by default. Turn it on with `ARK(supervision="experimental")`.

The flow:

1. your agent proposes an action
2. ARK checks the constraint that applies
3. ARK checks the trusted evidence you provide
4. ARK returns a verdict
5. on a verdict other than ALLOW, the agent gets feedback and decides again
6. your integration runs the action only after ALLOW

Verdicts are `ALLOW`, `REJECT`, `REQUIRE_EVIDENCE`, and `RECOVERY_EXHAUSTED`.

```python
from ark import ARK

with ARK(supervision="experimental").trace("book a flight") as run:
    verdict = run.check(
        proposed_action={"option": "A"},
        constraint="rank",
        evidence={
            "requested_rank": 2,
            "evidence_complete": True,
            "options": [{"id": "A", "price": 163}, {"id": "B", "price": 290}],
        },
        tool="book",
    )
    if verdict.allowed:
        ...  # run the tool
    else:
        # the agent reads the verdict and the feedback, then proposes the next action itself
        ...
```

The agent stays the author. ARK returns a verdict and feedback grounded in the evidence. It does not write the next action. The agent reads that feedback and decides what to do next.

Supervision is not a correctness guarantee. It does not make an agent safe, and it does not catch every bad action. It gives the runtime one place to check a proposed action against a rule and trusted evidence before the action runs.

## LangGraph

There is a real integration with LangGraph. You keep your LangGraph agent, your model, and your tools.

```bash
pip install "ark-agent-runtime[langgraph]"
```

```python
from ark.integrations.langgraph import ArkCallbackHandler, ark_supervise_tool
```

To observe, pass `ArkCallbackHandler(run)` in the callbacks and ARK records the model and tool decisions from LangChain's callback events.

To supervise, wrap a tool with `ark_supervise_tool(run, tool, constraint=..., evidence=...)`. Before the real tool runs, ARK checks the proposed action. On a verdict other than ALLOW the tool does not run, and ARK returns feedback grounded in the evidence as the tool result. LangGraph feeds that back to the model, and the model decides what to do next.

In a live run with a real OpenAI model:

- the model proposed action A
- ARK rejected A before its side effect
- the feedback went back to LangGraph
- the model proposed action B
- ARK allowed B
- only B ran

ARK did not write action B. The model did. This shows the mechanism can supervise a real external agent framework while the agent keeps authorship. It does not show that ARK improves every LangGraph agent.

## Evidence

Two separate results.

**A scoped benchmark.** In a paired K=16 tau bench airline evaluation, on one constrained recovery failure class, ARK raised task success from 6.25% to 81.25%, which is 1 of 16 up to 13 of 16.

| supervision | tasks passed |
|-------------|--------------|
| OFF         | 1 of 16      |
| ON          | 13 of 16     |

This is one constrained recovery failure class in the tau bench airline research environment. tau bench airline is a research benchmark, not an airline. It is not a general reliability result. It does not mean ARK improves all agents, and it does not solve hallucinations. In the same evaluation there were nine directly attributable recoveries and zero observed regressions, rank was satisfied on all 16 of 16 trials, and there were zero false rejects and zero evidence leakage.

**A real external agent.** The LangGraph live run above shows the same mechanism working around a real agent framework while the model keeps authorship.

One result proves a mechanism on a scoped benchmark failure class. The other proves the mechanism can supervise a real external framework. They are different claims.

## Supported platforms

CI builds a wheel for each of these and fresh installs it in a clean environment outside the repo:

- macOS Apple Silicon (arm64)
- macOS Intel (x86_64)
- Linux x86_64
- Linux arm64
- Windows x86_64

Each wheel carries the Go bridge for its platform.

## Also in ARK

When ARK runs the workload itself with `ark.run`, it also routes each step to a model and can run structural checks on code output, such as compile and lint. Those show up in the same telemetry. They are secondary to the observability and supervision story above.

## Alpha

Version 0.1.0a1. The API can still change. Supervision is experimental and off by default.

## License

The `ark-agent-runtime` package, meaning the Python SDK and the bundled Go bridge, is licensed Apache-2.0. The license ships inside the wheel.

## Feedback

Issues and ideas: https://github.com/atripati/ark/issues
