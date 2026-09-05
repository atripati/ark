# ARK

Runtime supervision and observability for AI agents.

Keep your model.
Keep your agent.
Keep your tools.
Add ARK around the runtime.

ARK sits between an agent's proposed consequential tool action and its execution. Before the action runs, ARK validates it against the runtime constraint that applies and the trusted evidence you provide, then returns one of `ALLOW`, `REJECT`, `REQUIRE_EVIDENCE`, or `RECOVERY_EXHAUSTED`. Only an allowed action runs. ARK does not author the replacement action. The agent remains the author.

```bash
pip install ark-agent-runtime
```

```python
from ark import ARK
```

Release-candidate software, version 0.1.0rc2. Supervision is experimental and off by default.

## Why ARK

An agent can know a rule and still propose the wrong tool call. Once that call runs, explaining it correctly afterward does not undo the side effect. The booking already happened. The row was already written.

ARK sits at that point, just before the call runs. It gives the runtime a place to look at the proposed action, the constraint that applies, and the trusted evidence, and then decide whether the call goes through.

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

```python
from ark import ARK
```

The core SDK has zero runtime Python dependencies and is tested in CI on Python 3.9 through 3.14. The wheel ships with the Go runtime bridge for your platform, so you do not need Go installed, the source repo, `PYTHONPATH`, or `ARK_BRIDGE_BIN`. The import stays `from ark import ARK`.

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

## Runtime supervision (experimental)

Supervision is experimental and off by default. Turn it on with `ARK(supervision="experimental")`.

The flow:

1. your agent proposes an action
2. ARK checks the constraint that applies
3. ARK checks the trusted evidence you provide
4. ARK returns a verdict
5. on a verdict other than ALLOW, the agent gets feedback and decides again
6. your integration runs the action only after ALLOW

| verdict | meaning |
|---|---|
| `ALLOW` | the action satisfies the constraint, or no constraint applies |
| `REJECT` | the action provably violates the constraint |
| `REQUIRE_EVIDENCE` | the constraint applies but the evidence is not enough to validate |
| `RECOVERY_EXHAUSTED` | the retry budget is spent and the action is still unsatisfied |

```python
from ark import ARK

with ARK(supervision="experimental").trace("book a flight") as run:
    action = {"option": "B"}
    verdict = run.check(
        proposed_action=action,
        constraint="rank",
        scope="booking-42",         # the resource/entity affected
        transaction="booking-42",   # one authorization lifecycle (retry is isolated per transaction)
        evidence={
            "requested_rank": 2,
            "evidence_complete": True,
            "options": [{"id": "A", "price": 163}, {"id": "B", "price": 290}],
        },
        tool="book",
    )
    if verdict.allowed:
        # PRE-EXECUTION gate: re-validate freshness/replay/action right before the side effect.
        cleared = run.consume(verdict, executed_action=action)
        if cleared.cleared:
            ...  # run the tool now, forwarding cleared.idempotency_key to a cooperating API
            run.record(of=verdict, executed=True, executed_action=action,
                       model="gpt-4o", input_tokens=..., output_tokens=...)
        else:
            ...  # stale before use — gather fresh evidence and re-check; do NOT execute
    else:
        # the agent reads the verdict and feedback, then proposes the next action itself
        ...
```

`scope` names the resource, `transaction` names one authorization lifecycle (retry budgets are isolated per transaction). On ALLOW, call `consume(...)` immediately before the side effect: it re-checks freshness, replay, and the exact action at *use* time and consumes the authorization once — execute only when `cleared.cleared` is true. `record(...)` requires the actual `executed_action` and matches it to what ARK authorized. The agent stays the author. ARK returns a verdict and feedback grounded in the evidence; it does not write the next action.

Boundaries (precise): ARK enforces this only for actions routed through the supervised path (`check` → `consume` → execute → `record`); a developer can bypass it by calling an unwrapped tool directly. ARK does not prove the supplied evidence is true, is not an OS-level security boundary, and cannot guarantee an external API executed only once unless that API honors the forwarded idempotency key. ARK provides **single-consumption authorization**, not "exactly-once execution": it prevents reuse of the same authorization through the supervised path; duplicate *external* execution additionally depends on the target system's idempotency semantics. `agent_id`/`transaction_id`/`scope` are identifiers supplied by the integration — bound and audited, but not authenticated by ARK.

Trusted evidence: the agent authors the proposed action; an application-configured `EvidenceProvider` (which the agent cannot select, replace, edit, or downgrade) establishes the facts through a separate trust channel; ARK deterministically checks the action against them. Evidence is trust-classified (`TRUSTED_PROVIDER` / `CALLER_SUPPLIED` / `AGENT_SUPPLIED`), and a protected constraint only ALLOWs on `TRUSTED_PROVIDER` evidence bound to the exact request/subject/tenant — so `check(action, evidence=agent_output)` can never authorize a protected action; only `check(action, provider="billing")` can. Precise claim: **ARK separates agent-authored proposals from evidence obtained through application-configured trust channels and binds authorization to that evidence deterministically — it does not prove the facts are true** (a provider can itself be wrong/compromised). No LLM judges evidence.

Durability: by default authorization state is in-memory (lost on process exit; an old authorization then fails closed). Set `ARK_AUTHZ_DIR=<dir>` to enable the durable store — the `ISSUED → CONSUMED` transition is then atomic and single-winner across restarts, crashes, and multiple ARK instances sharing that directory, retry-exhaustion survives restart, and a store failure fails closed. After a crash, a CONSUMED authorization whose outcome is unknown is reported as AMBIGUOUS for reconciliation — never silently retried.

Platforms: ARK supervision (including in-memory authorization) is supported on Linux, macOS, and **Windows**. The **durable** `ARK_AUTHZ_DIR` store is **POSIX-only** (Linux/macOS on a local filesystem): its durability requires a parent-directory `fsync`, which Windows does not permit. On Windows, requesting the durable store **fails closed** with an explicit unsupported-platform error rather than degrading silently — in-memory supervision is unaffected.

Supervision is not a correctness guarantee. It does not make an agent safe, and it does not catch every bad action. It gives the runtime one place to check a proposed consequential action against a constraint and trusted evidence before the action runs.

## LangGraph

There is a real integration with LangGraph. You keep your LangGraph agent, your model, and your tools.

```bash
pip install "ark-agent-runtime[langgraph]"
```

```python
from ark.integrations.langgraph import ArkCallbackHandler, ark_supervise_tool
```

The extra requires Python >=3.10 (LangChain and LangGraph 1.x declare `requires-python >=3.10`) and is tested in CI on Python 3.10 through 3.14. Core ARK on 3.9 is unaffected; only the LangGraph extra needs 3.10+.

To observe, pass `ArkCallbackHandler(run)` in the callbacks and ARK records the model and tool decisions from LangChain's callback events. To supervise, wrap a tool with `ark_supervise_tool(run, tool, constraint=..., evidence=...)`: before the real tool runs, ARK checks the proposed action, and on a verdict other than ALLOW the tool does not run and ARK returns evidence-grounded feedback as the tool result, which LangGraph feeds back to the model.

### Verified authorship, live with a real OpenAI model

```
model proposes A
   -> ARK REJECT
   -> A does not execute
   -> feedback returns to the model
   -> model proposes B
   -> ARK ALLOW
   -> only B executes
```

B was authored by the model, not generated by ARK. This shows the mechanism can supervise a real external agent framework while the agent keeps authorship. It does not show that ARK improves every LangGraph agent.

## Reporting and observability

Every run returns a canonical `RunResult`. A decision can carry the model and provider, input and output tokens, cost (which ARK derives from tokens and model when you do not pass one), the tool or proposed action and its outcome, a supervision verdict, executed state, retry number, latency, and a stable id that links a proposed action to the action that ran. Run totals include total cost, total tokens, and cost grouped by model, by tool, and by action. Missing fields stay empty; ARK does not fill them in.

```python
run.report()               # readable summary of the run
run.report(verbose=True)   # adds per-decision evidence and reported-vs-derived provenance
result = run.result
result.to_dict()           # the canonical RunResult as a dict
result.to_json()           # the canonical RunResult as JSON
```

`report()` prints run status, the model and tool decisions in order, costs and tokens, supervision verdicts, executed state, and retries. `verbose=True` adds the evidence behind each verdict and, from the trace session, the provenance of each fact (what your runtime reported versus what ARK derived). `result.report()` prints the same summary when you only have the `RunResult`.

`cost_by_supervision` is the cost of decisions that carry a supervision verdict. It is not necessarily ARK's causal incremental overhead.

## Concurrency and transport

The runtime session transport is serialized per session, so concurrent callers cannot interleave on the single line protocol. This is tested with parallel LangGraph tool fan-out. A verified six-way parallel fan-out produced six concurrent tool decisions with unique decision ids, a successful run, and no JSON corruption.

A deliberately hung bridge is subject to a bounded timeout: the process is terminated, and the dead session then refuses further calls rather than consuming a stale or late response as the answer to a later command.

## Platforms and fresh install

All five platform wheels are built and fresh-installed in CI, each carrying the bundled Go runtime bridge:

- macOS arm64
- macOS x86_64
- Linux x86_64
- Linux arm64
- Windows x86_64

The release candidate was installed into a clean environment outside the source repo, with `PYTHONPATH` and `ARK_BRIDGE_BIN` unset. There ARK imported from site-packages, the bundled bridge was discovered automatically and executed `ARK().run()`, the canonical `RunResult` telemetry came back, cost reconciliation passed, `ark.trace()` worked, and supervision stayed off by default.

## Python compatibility

| package | Python | notes |
|---|---|---|
| core `ark-agent-runtime` | 3.9 to 3.14 | zero runtime dependencies, tested in CI |
| `[langgraph]` extra | 3.10 to 3.14 | LangChain and LangGraph 1.x require >=3.10 |

## Evidence

Two separate results, held to their own scope.

### A scoped benchmark: tau-bench airline, K=16

Paired evaluation on one constrained recovery failure class.

| supervision | tasks passed | rate |
|---|---|---|
| OFF | 1 of 16 | 6.25% |
| ON | 13 of 16 | 81.25% |

That is +75 percentage points, with nine directly attributable recoveries and zero observed regressions. Rank was satisfied on all 16 of 16 trials, with zero false rejects and zero evidence leakage.

Scope: this is one constrained recovery failure class in the tau-bench airline research environment. tau-bench airline is a research benchmark, not an airline. It is not evidence that ARK improves every agent or every workload, and it does not solve hallucinations.

### A small local paired evaluation

A small local paired mechanism and regression check over five action-validation cases. OFF passed 0 of 5 and ON passed 5 of 5.

| classification | count |
|---|---|
| ARK recovery (a non-ALLOW intervention, then ALLOW) | 5 |
| safe no-op (ON correct with no intervention) | 0 |
| ARK regression (OFF passed, ON failed) | 0 |
| failed recovery (both failed) | 0 |
| unattributed ON win | 0 |

Every one of the five ON successes contained an actual non-ALLOW intervention followed by a later ALLOW, so all five are attributable and none are unattributed. Case 4, for example, went REJECT, REJECT, REJECT, then ALLOW. This is a small local mechanism and regression check, not the headline benchmark, and not a general 100% reliability claim.

### How recoveries are attributed

An OFF-fail with an ON-pass is counted as an ARK recovery only when a non-ALLOW ARK intervention precedes a later successful ALLOW. An ON win with no intervention is classified as unattributed, not credited to ARK.

## Safety of claims

- Supervision is experimental and off by default.
- ARK is not a correctness guarantee.
- ARK does not solve hallucinations in general.
- The evidence here applies to scoped action-validation workloads, not to arbitrary agents or tasks.

## Also in ARK

When ARK runs the workload itself with `ark.run`, it also routes each step to a model and can run structural checks on code output, such as compile and lint. These appear in the same telemetry and are secondary to the supervision and observability story above.

## License

The `ark-agent-runtime` package, meaning the Python SDK and the bundled Go bridge, is licensed Apache-2.0. The license ships inside the wheel.

## Feedback

Issues and ideas: https://github.com/atripati/ark/issues
