# Constrained action supervision (experimental)

A generic, domain-agnostic mechanism that supervises an agent's *proposed* actions against a
runtime-derived constraint, without ever authoring the action itself. Ported from a research
result that reproducibly raised task success by enforcing a stated constraint the agent had
otherwise violated.

**Experimental.** Off by default; opt-in only. Contains no domain, task, or benchmark specifics.

## The mechanism

```
proposed action
  → identify the applicable runtime constraint       (is any constraint in force for this action?)
  → gather / inspect trusted runtime evidence        (what does the environment actually show?)
  → validate the proposed action against the constraint
      → ALLOW              if the action satisfies the constraint
      → REQUIRE_EVIDENCE   if the constraint applies but the evidence is insufficient to decide
      → REJECT (+reason)   if the action provably violates the constraint
  → bounded constrained retry                         (surface the rejection; the agent proposes again)
      → repeat validation on each new proposed action
      → RECOVERY_EXHAUSTED once the retry budget is spent while still unsatisfied
  → execute only a proposed action that satisfies the constraint
```

The supervisor **never constructs the agent's final action**. On a rejection it returns the
violated constraint and the supporting runtime evidence; the agent retains authorship and
proposes the next action itself. A known-violating action is never executed after the budget —
the outcome is RECOVERY_EXHAUSTED, not a silent pass-through.

## Outcomes (verdicts)

| verdict | meaning | executes? |
|---|---|---|
| `ALLOW` | constraint not applicable, or the proposed action satisfies it | yes |
| `REJECT` | the proposed action provably violates the applicable constraint | no — agent retries |
| `REQUIRE_EVIDENCE` | the constraint applies but the evidence is insufficient to validate | no — gather more, agent retries |
| `RECOVERY_EXHAUSTED` | retry budget spent while still unsatisfied | no — never executes a known-violating action |

## Interfaces (generic hooks)

- **Constraint** — `Applicable(proposed, evidence) bool` and `Validate(proposed, evidence) Verdict`.
  Domain code implements this; the mechanism knows nothing about the domain.
- **Evidence** — an opaque bag of trusted runtime facts the caller has gathered (never the
  agent's own claims; never task gold or evaluator data).
- **ProposedAction** — the action the agent wants to execute, as opaque fields.
- **Decision / audit** — the verdict plus a full audit record (see below).
- **Supervisor** — runs one evaluation given the proposed action, the evidence, the current
  retry count, and the budget; emits the verdict and audit record. Retry-loop state (the count)
  is owned by the caller; the supervisor decides `REJECT` vs `RECOVERY_EXHAUSTED` from it.

## Auditability (every intervention records)

original proposed action · applicable constraint · trusted evidence used · verdict ·
rejection reason · retry number · (the subsequent proposed action, recorded by the caller) ·
whether it eventually executed · cost/latency added.

## Non-goals (explicitly out of scope)

No task/benchmark identifiers, no expected/gold values, no evaluator data, no domain constants.
No planner, no cost optimization, no action synthesis. The mechanism only *judges and gates*
actions the agent authored; it does not create them.

## Reference constraint shipped: rank-selection (generic)

A single generic constraint is included to exercise the mechanism: *"the proposed option must be
the rank-N cheapest within the complete set of retrieved, priced options."* It operates purely on
opaque option ids + numeric prices + a requested rank + an evidence-completeness flag — with no
domain values baked in. It is the proven constraint from the research result, generalized.
