# Design: external-agent integration (attach ARK around your runtime)

> Design only — no implementation. Does not change the telemetry contract, supervision
> engine, routing, or cost accounting; it reuses them with externally-reported decisions.

## The inversion

| | current `ark.run(task)` | this milestone |
|---|---|---|
| who executes | **ARK's** internal Go agent | **the developer's** agent/model/tools |
| ARK's role | executor + observer + supervisor | **observer + in-loop supervisor** |
| model/tool telemetry | ARK sees it (it runs the loop) | ARK **cannot see it** — it is reported |

Because ARK is no longer in the execution path, it observes **almost nothing about the
model/tool execution automatically**. What ARK still owns is *derivation* (canonical
structure, cost, aggregates), the *supervision engine*, and *RunResult assembly*. The raw
per-step facts must be reported by the developer's runtime. Naming this honestly is the
core of the design — no "ARK automatically watches your agent" overclaim.

## Three candidate interfaces

| interface | dev effort | framework knowledge ARK needs | gates actions? | works across frameworks? |
|---|---|---|---|---|
| `agent = ark.wrap(agent)` | 1 line | **high** — must know how each framework exposes model/tool calls to intercept them | implicit | **No** — requires a per-framework adapter (the very framework-specific reimplementation we must avoid) |
| `with ark.trace(...) as run:` | a few explicit reports | **none** — the developer reports events | explicit `run.check()` | **Yes** — generic |
| raw middleware/hooks (`session.decision(...)`, `session.check(...)`) | most explicit | **none** | explicit | **Yes**, but verbose |

`wrap` cannot be the generic core: there is no universal "agent" interface across LangGraph
/ CrewAI / OpenAI Agents, so auto-interception *is* framework-specific code. `wrap` therefore
belongs later as **per-framework sugar built on the session**, not as the generic interface.

## Recommendation: a session, surfaced as `ark.trace(...)`

The smallest generic interface is a **run session** (the raw middleware) with two verbs —
`check` (supervision gate) and `record` (telemetry) — surfaced ergonomically through a
context manager that owns the lifecycle:

```python
class ARK:
    def trace(self, task: str, *, task_type=None, supervision="off",
              provider=None, model=None) -> "RunSession": ...   # context manager

class RunSession:
    task: str
    # 1) supervision gate (only when you pass a constraint + evidence). Synchronous,
    #    pre-execution, non-authoring. Returns a Verdict that is also a decision handle.
    def check(self, *, action, proposed, constraint, evidence,
              tool=None) -> "Verdict": ...
    # 2) report a decision's telemetry (the executed action, or an unsupervised step).
    #    `of=` links execution telemetry to a prior check so ONE DecisionRecord carries
    #    both the supervision verdict and the execution facts.
    def record(self, *, action, model=None, tool=None, tool_args=None,
               input_tokens=None, output_tokens=None, cost=None, latency_ms=None,
               routing_reason=None, verification=None, outcome=None,
               executed=True, of: "Verdict|None"=None) -> "DecisionRef": ...
    # available after the block (or via finish()):
    result: "RunResult"

class Verdict:                 # returned by check()
    verdict: str               # ALLOW / REJECT / REQUIRE_EVIDENCE / RECOVERY_EXHAUSTED
    allowed: bool
    suggested: str | None      # runtime-derived option — EVIDENCE, not an authored action
    retry_number: int
```

- `ark.trace(task=...)` → **observe-only** (report decisions, get `RunResult`; no supervision).
- `ark.trace(task=..., supervision="experimental")` → also enables `run.check()`.
- The developer's loop stays in control; ARK never drives it and never authors an action.

## 10-line developer example (observe-only)

```python
import ark
with ark.trace(task="find the top Python web frameworks") as run:
    for step in my_agent.plan(run.task):            # your framework, your model, your tools
        out = step.execute()                        # you run it
        run.record(action=step.kind, model=out.model, tool=out.tool,
                   input_tokens=out.in_tok, output_tokens=out.out_tok,
                   latency_ms=out.ms, routing_reason=out.routing_reason, outcome="success")
result = run.result                                 # canonical RunResult (same schema as ark.run)
print(result.success, result.total_cost, [d.model for d in result.decisions])
```

Supervised step (adds the gate; your agent re-authors on a non-ALLOW verdict):

```python
v = run.check(action="tool_call", tool="book", proposed={"option": "A"},
              constraint="rank", evidence=my_runtime_evidence())     # -> REJECT, suggested="B"
if v.allowed:
    out = step.execute()
    run.record(action="tool_call", tool="book", model=out.model,
               input_tokens=out.in_tok, output_tokens=out.out_tok, of=v)
else:
    step.replan(hint=v.suggested)                   # YOUR agent proposes the next action
```

## Lifecycle

```
ark.trace(task) ── enter ─► ARK opens a run (run_id, started_at); telemetry.Builder starts
   │
   │   ┌─────────────── per agent step (your loop) ───────────────┐
   │   │  run.check(proposed, constraint, evidence)               │
   │   │        └─► [Go supervise engine] ─► ALLOW / REJECT /      │
   │   │            REQUIRE_EVIDENCE / RECOVERY_EXHAUSTED          │
   │   │        REJECT/REQUIRE ─► your agent re-authors ──► loop   │
   │   │        ALLOW          ─► you execute in your framework    │
   │   │  run.record(action, model, tokens, tool, latency, …)     │
   │   │        └─► DecisionRecord (ARK assigns decision_00N,      │
   │   │            derives cost from tokens+model)                │
   │   └──────────────────────────────────────────────────────────┘
   │
   └── exit ─► ARK finishes ─► [Go telemetry.Builder (+ cost pricing)] ─► canonical RunResult
             success/termination from the block's exit; all aggregates derived from decisions
```

## What ARK observes / derives **automatically** (in this mode)

ARK is not in the execution path, so "automatic" means **derivation and the engine**, not
interception:
- **decision identity & ordering** — `decision_001…`, `sequence`, timestamps.
- **cost** — derived per decision from the reported `{model, input_tokens, output_tokens}`
  using ARK's existing pricing tables (`cost.ModelPricing`); no cost reimplementation.
- **run-level aggregates** — `total_cost/tokens/latency`, `cost_by_model/tool/action/
  supervision`, `routing`/`tool`/`supervision` summaries — all derived from the decisions.
- **supervision verdicts + audit** — computed by the existing supervise engine from the
  supplied proposal + constraint + evidence; retry budget tracked; `RECOVERY_EXHAUSTED`.
- **redaction & provider status** — tool args redacted; providers reported `configured/absent`.
- **RunResult assembly** — the same canonical contract as `ark.run`.

## What the developer **supplies** (ARK cannot see their framework)

- **the proposed action and the executed action** (what the agent decided/did);
- **model name**, **input/output tokens**, **tool name + args**, **latency** per step;
- **routing_reason** *if their framework routes* (ARK does **not** route in this mode);
- **verification** *if their framework verifies* (ARK does **not** verify in this mode);
- **task / task_type**, and success/termination (or let the context manager infer them);
- for supervision: the **constraint** and the **trusted runtime evidence** (e.g. the retrieved,
  priced options) — ARK validates but never gathers the evidence itself.

Fields the framework does not provide stay **absent/None** — never fabricated. In particular,
`routing_reason` and `verification` are frequently None here, because those are ARK-agent
behaviors, not something ARK can observe about an external agent.

## How supervision participates in the live agent loop

`run.check(...)` is a **synchronous, pre-execution, non-authoring gate**:
1. the developer calls it with the agent's *proposed* action + a runtime-derived constraint
   + the *trusted evidence* the framework already gathered;
2. ARK evaluates it with the **unchanged** supervise engine and returns a verdict
   (`ALLOW/REJECT/REQUIRE_EVIDENCE/RECOVERY_EXHAUSTED`) plus a runtime-derived suggestion;
3. on `ALLOW`, the developer executes in their framework and reports telemetry (`record(..., of=v)`);
4. on `REJECT`/`REQUIRE_EVIDENCE`, **the developer's agent re-authors** the next action
   (optionally using `v.suggested` as evidence) and calls `check` again — ARK tracks the
   retry count and returns `RECOVERY_EXHAUSTED` when the budget is spent, at which point the
   developer stops executing that action.

ARK is *in* the loop (it gates before execution) but never *drives* it and never *creates*
the replacement action. Agent authorship and framework control are preserved.

## How `RunResult` is produced

The session accumulates lightweight decision reports (+ any supervision verdicts). On exit/
`finish()`, it hands them to the Go side, which runs the **existing** `telemetry.Builder`
(deriving cost via `cost.ModelPricing` for any decision reported with tokens but no cost) and
returns the **same canonical `RunResult`** produced by `ark.run` — one contract, two
production paths (ARK's agent, or an external agent reporting through the session).

## Smallest Go-side addition needed (for the implementation milestone, not now)

- **One new bridge request kind, `build_run`**: `{run metadata, decisions[]}` → canonical
  `RunResult` (via `telemetry.Builder` + `cost.ModelPricing`). The existing `supervise` kind
  already serves `check`. No change to the telemetry contract, supervision engine, routing,
  or cost accounting — the session just feeds externally-authored decisions into the same
  machinery. Transport stays an implementation detail (subprocess now; a persistent
  process/service later) behind the unchanged Python API.

## Explicitly deferred

No LangGraph / CrewAI / OpenAI-Agents adapter, no `ark.wrap()` implementation, no dashboards
/ cloud / auth / billing, no new validators, no cost optimization. Those are downstream of
this generic session interface.
