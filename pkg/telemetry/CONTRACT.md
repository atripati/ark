# ARK runtime data contract (telemetry)

The canonical, machine-readable shape of one ARK run. **This contract is intended to become
the foundation of the future ARK SDK.** It is observability only — it changes no runtime
behavior, makes no cost-effectiveness judgments, and never serializes secrets.

```
Run (RunResult)
  └── Decisions[] (DecisionRecord)          # one stable record per meaningful decision
        ├── model            (model + routing_reason)
        ├── tool/action      (tool + redacted tool_args_ref)
        ├── verification     (method, passed, score, confidence, issues)
        ├── supervision      (constraint, scope, verdict, proposed+fingerprint,
        │                     evidence ref/fingerprint/source/version/observed_at, retry, executed)
        ├── outcome          (outcome, error, executed)
        └── cost             (tokens, input/output/model/tool cost, total, latency)
```

## Two objects

- **`DecisionRecord`** — one meaningful runtime decision, with a stable id (`decision_001`,
  `decision_002`, …) and monotonic `sequence`. Fields that do not apply are left nil/zero;
  nothing is fabricated. It describes *what happened*, not whether it was worth it.
- **`RunResult`** — one run. Its aggregates (`total_*`, `cost_by_*`, `supervision`, `routing`,
  `tools`) are **derived from the decisions** in `Finish` — a single source of truth.

## Decision-level cost attribution (factual)

Each decision persists `input/output tokens`, `input/output/model cost`, `tool cost` (if
known), `total_cost`, and `latency`. `model_cost + tool_cost == total_cost`. Run views:

| view | meaning |
|---|---|
| `cost_by_model` | partition of total by model |
| `cost_by_action` | partition of total by action (`tool_call`, `complete`, `retry`, …) |
| `cost_by_tool` | tool-associated decisions only (their total, keyed by tool) |
| `cost_by_supervision` | cost of decisions carrying a supervision verdict, keyed by verdict |

No judgment is attached: a failed or rejected step's cost is recorded as its cost — never
labeled "wasted" or "saved". Interpretation is a later concern.

## Where it comes from (existing ARK sources)

| DecisionRecord field | ARK source |
|---|---|
| action / tool / tokens / latency | `runtime.TaskResult.Steps[]StepRecord` |
| input/output/model cost | `cost.CostReport.Steps[]StepCost` (the "decision cost graph") |
| model / routing_reason | `router.Decision{ModelUsed, Reason, PriorModel}` |
| verification | `runtime.GovernorVerdict{Confidence}` + `runtime.VerificationResult{Passed,Score,Issues}` |
| supervision | `supervise.Decision`/`AuditRecord` via `SupervisionFromDecision` |
| outcome / success / output | `runtime.TaskResult{Success, Output}` |

`RunResult.total_cost`, `cost_by_tool`, and `cost_by_action` reconcile with the existing Cost
Report for the same run (verified in tests against a real run: $0.005348 / 1533 tokens).

## Current-run vs historical (kept separate)

`RunResult` is **this run only**. The Model Capability Registry / governor history (lifetime
call counts, success rates, averages) is **not** part of it and must not be mixed in — the
Builder never reads governor state. A future SDK can expose both, distinctly.

## Supervision in the same record

The graduated constrained-supervision mechanism (`pkg/supervise`, experimental, off by
default) is not a separate observability universe. A supervised action is one `DecisionRecord`
whose `supervision` block records the constraint, the **scope** it was bound to, the verdict
(`ALLOW`/`REJECT`/`REQUIRE_EVIDENCE`/`RECOVERY_EXHAUSTED`), the proposed option/kind and a
**fingerprint** of the exact proposed action, a **reference + content fingerprint** of the
trusted evidence together with its provenance (`evidence_source`, `evidence_version`,
`evidence_observed_at_unix`) — never the raw bag — the retry number, and whether it executed.
The evidence reference is the caller's `evidence_id` when supplied, else the content
fingerprint (never a constant placeholder), so an auditor can tie a verdict to the exact
evidence state that produced it. A reject→retry→allow chain is a sequence of distinct
decisions; ARK still never authors the action — the suggested option is evidence, and the agent
re-proposes. Supervision fails **closed**: an unknown constraint or malformed evidence is
refused (an error), never recorded as an `ALLOW`.

The block also carries the **authorization lifecycle** for a consequential action:
`transaction_id` (the retry-isolated lifecycle), the proposing `agent_id` (on the DecisionRecord),
a REDACTED view of the proposed parameters (`proposed_fields_redacted`, secret-like keys masked),
`evidence_expires_at_unix`, the `auth_state` (`ISSUED`→`CONSUMED`→`COMPLETED`), a stable
`idempotency_key`, and `issued_at_unix`/`consumed_at_unix`/`executed_at_unix` — each stamped at
its real event time (UTC), not at run finish. Together these make a consequential authorization
reconstructable from telemetry alone without leaking secrets.

## Security / telemetry hygiene

Telemetry never serializes secrets. Providers are reported as `configured`/`absent`
(`ProviderStatus`), never keys/tokens/headers. Tool arguments are stored as a redacted
reference (`RedactToolArgs` masks token/key/secret/authorization-like fields). The contract is
designed so raw secrets are never required in the record.

## Emit / read

`RunResult.JSON()` produces the canonical JSON; `ParseRunResult` reads it back (round-trips in
tests). The human-readable CLI output can be rendered from this same structure — the intent is
one underlying truth, not two. (Wiring the live `ark run` emitter is a trivial follow-up left
for the SDK milestone to avoid entangling unrelated in-progress work.)
