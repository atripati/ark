# Constrained supervision (experimental)

A generic mechanism that supervises an agent's **proposed** actions against a runtime-derived
constraint, and gates execution — without ever authoring the action itself. Full design in
[SPEC.md](SPEC.md).

> **Experimental. Disabled by default.** Nothing here runs unless a caller opts in. It does not
> change any existing ARK behavior when the flag is off.

## What it does

Given an action the agent wants to execute, the supervisor:

```
proposed action (+ scope binding it to one transaction/entity)
  → is the named constraint registered?             (unknown -> fail closed, error)
  → strictly decode + validate the trusted evidence (malformed/typo'd -> fail closed, error)
  → is freshness required and satisfied?            (stale/missing -> REQUIRE_EVIDENCE)
  → validate the proposed action against the constraint
      → ALLOW / REJECT / REQUIRE_EVIDENCE
  → bounded constrained retry, keyed per (scope, constraint)
  → a valid action executes; a still-non-ALLOW one at the budget is RECOVERY_EXHAUSTED
  → the outcome is audited
```

ARK **validates and constrains; it does not construct the agent's final action.** On a rejection
it returns the violated constraint and the supporting runtime evidence (including any
runtime-derived target). The agent re-authors the next action itself. A non-ALLOW action is
never executed after the budget.

**Fail closed.** `ALLOW` is returned only when the proposal is *provably* valid (or the constraint
*provably* does not govern the action). An unknown constraint, malformed/typo'd evidence, or any
inability to establish validity never becomes `ALLOW` — see the verdict table.

## Verdicts

| verdict | meaning | executes? |
|---|---|---|
| `ALLOW` | the proposal provably satisfies the constraint, or the constraint provably does not apply | yes |
| `REJECT` | the action provably violates the applicable constraint | no — the agent retries |
| `REQUIRE_EVIDENCE` | the constraint applies but the evidence is insufficient/unverifiable/stale | no — gather more, retry |
| `RECOVERY_EXHAUSTED` | retry budget spent while still non-ALLOW | no — never executes a non-ALLOW action |
| *(error)* | unknown constraint or malformed supervision configuration | no — refused at the boundary, not a verdict |

An unknown constraint is **not** treated as "not applicable"; not-applicable is a positive,
trusted determination, whereas an unknown constraint is a configuration error and fails closed.

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

## Applicability (three-state, never fail open)

A constraint's `Applicable` returns **APPLIES**, **DOES_NOT_APPLY**, or **CANNOT_DETERMINE**.
`DOES_NOT_APPLY` → ALLOW (only a positive "does not govern" determination); `CANNOT_DETERMINE`
→ REQUIRE_EVIDENCE (missing/malformed data never becomes ALLOW). A third-party constraint that
cannot see a required field must return `CANNOT_DETERMINE`, so a missing field can never silently
authorize. A constraint may implement the optional `EvidenceValidator` interface to co-locate its
required-field checks (no central switch to forget).

## Identity binding, mandatory action binding, replay, and idempotency

Each check carries a **`scope`** — a generic id for the transaction/entity/decision-point being
supervised. Retry state is keyed per `(scope, constraint)`, so one entity's rejections can never
consume another's budget. Each decision records a fingerprint of the exact proposed action and
evidence it evaluated. ARK enforces, itself: a non-ALLOW decision is never recorded as executed;
**confirming execution of an ALLOW is mandatory-bound** — the caller must present the actual
structured executed action, and ARK re-canonicalizes it (via the one canonical `Fingerprint`) and
requires it to match the authorized action; a missing or different action is refused (a bare
fingerprint is not accepted). A decision executes at most once. The integration remains
responsible for actually gating on the verdict and reporting the action it truly executed — ARK
sees only what is routed through it, and it is not an OS/security boundary.

## Audit trail

Every decision emits an audit record with: the constraint, the scope, whether it applied, the
original proposed action **and its fingerprint**, a **fingerprint + provenance (id/source/version/
observed_at) of the trusted evidence** (never the raw evidence), the verdict, the rejection reason,
the retry number, the runtime-derived suggestion (evidence — **not** an authored action), whether
it executed, and per-decision timestamps captured at the actual event time (verdict time on the
record, plus a distinct execution time) — not collapsed to run-finish time. Callers add cost/latency
they observe. Secrets in tool args are redacted; nothing task-, gold-, or evaluator-specific is
recorded.

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
