# Constrained supervision (experimental)

A generic mechanism that supervises an agent's **proposed** actions against a runtime-derived
constraint, and gates execution — without ever authoring the action itself. Full design in
[SPEC.md](SPEC.md).

> **Experimental. Disabled by default.** Nothing here runs unless a caller opts in. It does not
> change any existing ARK behavior when the flag is off.

## What it does

Given an action the agent wants to execute, the supervisor:

```
proposed action
  → identify the applicable runtime constraint
  → gather / inspect trusted runtime evidence
  → validate the proposed action against the constraint
      → ALLOW / REJECT / REQUIRE_EVIDENCE
  → bounded constrained retry (surface the rejection; the agent proposes again)
  → a valid action executes; a still-invalid one at the budget is RECOVERY_EXHAUSTED
  → the outcome is audited
```

ARK **validates and constrains; it does not construct the agent's final action.** On a rejection
it returns the violated constraint and the supporting runtime evidence (including any
runtime-derived target). The agent re-authors the next action itself. A known-violating action is
never executed after the budget.

## Verdicts

| verdict | meaning | executes? |
|---|---|---|
| `ALLOW` | not applicable, or the action satisfies the constraint | yes |
| `REJECT` | the action provably violates the applicable constraint | no — the agent retries |
| `REQUIRE_EVIDENCE` | the constraint applies but the evidence is insufficient to decide | no — gather more, retry |
| `RECOVERY_EXHAUSTED` | retry budget spent while still unsatisfied | no — never executes a known-violating action |

## Enabling (off by default)

```bash
# Refused without the flag:
echo '<request-json>' | ark supervise            # -> "experimental and off by default" (exit 2)

# Enabled explicitly:
echo '<request-json>' | ARK_EXPERIMENTAL_SUPERVISION=1 ark supervise
# --batch reads one request per line and emits one decision per line (JSONL)
```

Example request / decision:

```bash
echo '{"constraint":"rank","proposed":{"option":"A"},
       "evidence":{"requested_rank":2,"evidence_complete":true,
                   "options":[{"id":"A","price":163,"is_direct":true},{"id":"B","price":290}]},
       "retry_count":0,"budget":4}' \
  | ARK_EXPERIMENTAL_SUPERVISION=1 ark supervise
# -> {"verdict":"REJECT","reason":"proposed option \"A\" is rank 1 ...","audit":{...,"suggested_from_evidence":"B","executed":false}}
```

## Audit trail

Every decision emits an audit record with: the constraint, whether it applied, the original
proposed action, the trusted evidence used, the verdict, the rejection reason, the retry number,
the runtime-derived suggestion (evidence — **not** an authored action), and whether it executed.
Callers add cost/latency they observe. Nothing task-, gold-, or evaluator-specific is recorded.

## Constraints must be grounded in runtime evidence

A `Constraint` decides applicability and validity **only** from trusted runtime evidence the
caller gathered — the retrieved options/state, and the like. It must **never** use the agent's own
claims, task gold, evaluator data, or benchmark constants. The shipped `RankConstraint` (rank-N
over a priced option set) is fully generic: opaque option ids + numeric prices + a requested rank
+ an evidence-completeness flag. Add new constraints by implementing the `Constraint` interface;
keep them domain-general and evidence-grounded.

## Provenance

This capability graduated from a research result that reproducibly enforced a stated constraint
an agent had otherwise violated. See, in the companion `ark-research` repo:
`FROZEN_REFERENCE.md` (research commit `1b761c7`) and `REPRODUCTION_K16_mainark.md` (the main-ARK
reproduction: mechanism behavior identical — rank enforced every trial, zero false rejects, zero
regressions, zero leakage). The mechanism was ported here unchanged; nothing was tuned to a
benchmark. This is an engineering capability, not a claim about all agents.
