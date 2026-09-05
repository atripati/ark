# Constrained action supervision (experimental)

A generic, domain-agnostic mechanism that supervises an agent's *proposed* actions against a
runtime-derived constraint, without ever authoring the action itself. Ported from a research
result that reproducibly raised task success by enforcing a stated constraint the agent had
otherwise violated.

**Experimental.** Off by default; opt-in only. Contains no domain, task, or benchmark specifics.

## Safety posture: FAIL CLOSED

ARK never returns `ALLOW` for a supervised action merely because it could not evaluate the
constraint. `ALLOW` is returned only when the proposal is *provably* valid, or the constraint
*provably* does not govern the action (an explicit, trusted applicability determination). Every
other outcome is non-ALLOW:

- unknown / unregistered constraint, or a malformed supervision payload  → **error** (refused; never a verdict the agent recovers from)
- the constraint applies but validity cannot be established              → `REQUIRE_EVIDENCE`
- the proposal provably violates the constraint                         → `REJECT`
- the retry budget is spent while still non-ALLOW                        → `RECOVERY_EXHAUSTED` (never executes)

A missing or typo'd evidence field is **not** silently coerced to a Go zero value that yields
ALLOW: strict decoding rejects unknown fields, and each constraint's required fields are
validated up front.

## The mechanism

```
proposed action (+ scope = resource, transaction = one authorization lifecycle)
  → is the named constraint registered?              (unknown -> fail closed, error)
  → strictly decode + validate the trusted evidence  (malformed -> fail closed, error)
  → is freshness required and satisfied?              (stale/missing/future -> REQUIRE_EVIDENCE)
  → validate the proposed action against the constraint
      → ALLOW              only if the action provably satisfies the constraint
      → REQUIRE_EVIDENCE   if the constraint applies but validity cannot be established
      → REJECT (+reason)   if the action provably violates the constraint
  → bounded constrained retry, keyed per (transaction, constraint)
      → surface the non-ALLOW; the agent proposes again
      → RECOVERY_EXHAUSTED once the retry budget is spent while still non-ALLOW
  → on ALLOW: an authorization is ISSUED
  → CONSUME (pre-execution gate): re-check freshness/replay/action at USE time, once
      → execute ONLY a cleared authorization → record result (COMPLETED)
```

Execution lifecycle: an ALLOW is not a licence to execute at any later time. It ISSUES an
authorization the integration must **consume immediately before the side effect**. Consume
re-validates against a fresh clock — the evidence freshness window still open, the verdict still
ALLOW, the action still matching, and the authorization not already consumed — and marks it
CONSUMED exactly once. A stale-at-use, replayed, or mismatched authorization is refused *before*
the side effect, not merely detected after.

The supervisor **never constructs the agent's final action**. On a rejection it returns the
violated constraint and the supporting runtime evidence; the agent retains authorship and
proposes the next action itself. A known-violating (or still-unverified) action is never
executed after the budget — the outcome is RECOVERY_EXHAUSTED, not a silent pass-through.

## Outcomes (verdicts)

| verdict | meaning | executes? |
|---|---|---|
| `ALLOW` | the proposal provably satisfies the constraint, or the constraint provably does not apply | yes |
| `REJECT` | the proposal provably violates the applicable constraint | no — agent retries |
| `REQUIRE_EVIDENCE` | the constraint applies but the evidence is insufficient/unverifiable/stale | no — gather more, agent retries |
| `RECOVERY_EXHAUSTED` | retry budget spent while still non-ALLOW | no — never executes a non-ALLOW action |
| *(error)* | unknown constraint or malformed supervision configuration | no — refused at the boundary |

An unknown constraint is **not** modeled as "not applicable." Not-applicable is a positive,
trusted determination that the constraint does not govern the action; an unknown constraint is a
configuration error and fails closed.

## Applicability is three-state (never fail open on uncertainty)

A constraint's `Applicable` returns one of **APPLIES**, **DOES_NOT_APPLY**, or
**CANNOT_DETERMINE**. A boolean would conflate "does not govern" with "cannot tell", and the
kernel would ALLOW on the latter. The kernel maps them: `APPLIES` → run `Validate`;
`DOES_NOT_APPLY` → `ALLOW` (ONLY as a positive determination that the constraint does not govern
this action); `CANNOT_DETERMINE` → `REQUIRE_EVIDENCE`, `Executed=false`. So a third-party
constraint (e.g. `refund_limit`) that cannot see a required field — a missing action `kind` or
amount — returns `CANNOT_DETERMINE` and the action fails closed; it can no longer slip through as
"not applicable".

## Identity binding (scope), action binding, and replay safety

Each check carries a `scope`: a generic identifier for the transaction/entity/decision-point
being supervised. `scope` names the affected resource; `transaction` names one authorization
lifecycle. Retry state is keyed per `(transaction, constraint)` — so two distinct transactions
for the *same* resource have independent budgets (transaction defaults to scope when the caller
does not distinguish them). Each decision records a fingerprint of the exact proposed action and
the exact evidence state it evaluated, so:

- ARK enforces (itself, in-process): a non-ALLOW decision is never recorded as executed;
  **confirming execution of an ALLOW is MANDATORY-bound** — at consume and at record the caller
  must present the actual structured executed action, ARK re-canonicalizes it (via the one
  canonical `Fingerprint`) and requires it to match the authorized action; a missing or different
  action is refused (fail closed), and a bare fingerprint is not accepted. An authorization is
  consumed at most once and completed at most once (in-process, per live session).
- The integration is responsible for: calling `consume` before the side effect and gating on the
  result, and reporting the action it truly executed. ARK sees only what is routed through it — it
  cannot stop a developer from bypassing it and calling an unwrapped tool directly.

### Idempotency: what ARK owns vs the external system

ARK prevents reuse of the *same authorization* through the supervised path (consume-once,
complete-once), and returns a stable `idempotency_key` per authorization. It does **not** and
cannot prevent an external API from performing the real side effect twice unless that API honours
the forwarded key. The truthful claim: *"ARK prevents reuse of the same authorization through the
supervised path; duplicate external execution additionally depends on the target system's
idempotency semantics."* The consumed-once guarantee is **in-process** by default, and **durable
across restart and across ARK instances** when a durable store is configured (see the durability
modes below). Prefer the phrase *"single-consumption authorization"* over "exactly-once execution".

## Interfaces (generic hooks)

- **Constraint** — `Applicable(proposed, evidence) Applicability` and `Validate(proposed,
  evidence) → (Verdict, reason, suggestion)`. Domain code implements this; the mechanism knows
  nothing about the domain. Return `DOES_NOT_APPLY` only as a positive determination; return
  `CANNOT_DETERMINE` when a required field is missing (never `DOES_NOT_APPLY`). `Validate`
  returns `ALLOW` only for a provably valid proposal. A constraint may also implement the
  optional `EvidenceValidator` interface to declare its required evidence fields co-located with
  the constraint, so adding a constraint cannot silently weaken validation.
- **Evidence** — trusted runtime facts the caller gathered (never the agent's own claims; never
  task gold or evaluator data), plus optional `meta` (evidence_id, source, version, observed_at,
  expires_at) ARK records and can enforce freshness against.
- **ProposedAction** — the action the agent wants to execute, as opaque fields.
- **Decision / audit** — the verdict plus a full audit record (see below).
- **Supervisor** — evaluates one request; retry-loop state (the count) is owned by the caller,
  keyed per (scope, constraint); the supervisor decides `REJECT` vs `RECOVERY_EXHAUSTED` from it.

## Auditability (every intervention records)

proposed action + its fingerprint + REDACTED parameters · scope · transaction id · agent id ·
applicable constraint · trusted evidence fingerprint + provenance (id/source/version/observed_at/
expires_at) · verdict · rejection reason · retry number · the runtime-derived suggestion
(evidence, not an authored action) · authorization state (ISSUED/CONSUMED/COMPLETED) + idempotency
key · issued_at / consumed_at / executed_at (each at its real time, UTC) · whether it executed ·
cost/latency. Raw evidence is not persisted; secret-like parameter keys are masked; safe
structured references are kept so an incident is reconstructable without leaking secrets.

### Durability modes and the persistence boundary

The authorization lifecycle lives behind a small `authz.Store` seam with two implementations:

- **In-memory (default):** state lives for the session's life, guarded by a session mutex (not by
  the transport happening to serialize). After process termination an old authorization becomes
  UNKNOWN and fails closed. Retry state also resets on restart in this mode.
- **Durable (opt-in via `ARK_AUTHZ_DIR`):** a filesystem store whose `ISSUED -> CONSUMED`
  transition is **atomic and single-winner** across threads, processes, and ARK instances sharing
  the directory (built on the OS `O_CREATE|O_EXCL` primitive — no external database, no cgo, so it
  works in the CGO_ENABLED=0 static wheel build). In this mode:
  - a CONSUMED authorization stays CONSUMED across restart/crash — never re-issued (DUR-01);
  - two instances cannot both consume one authorization (DUR-02/10);
  - retry exhaustion survives restart — a restart cannot mint a fresh budget (DUR/Phase 6);
  - a store error (unavailable/locked/permission/corrupt/incompatible schema) is an explicit
    error that **fails closed** — never a cleared execution (DUR-05/12/14).

**Crash recovery is explicit, never fabricated.** Each transition is atomic, so after a crash the
store is in exactly one of {ISSUED, CONSUMED, COMPLETED}. **CONSUMED means "claimed for execution;
the external outcome may not be known"** — the `status` query reports this as AMBIGUOUS and asks
the operator to reconcile against the target system using the idempotency key before any retry.
ARK does not pretend to know whether the external side effect occurred.

- **External tool:** duplicate real side effects can only be prevented by a cooperating idempotent
  target API, via the forwarded `idempotency_key` (the stable authorization id).

**Authorization namespace (no aliasing).** The authorization id is derived from the FULL
security-relevant tuple — `namespace` (tenant/application), `transaction`, `scope`, `constraint`,
action fingerprint, evidence fingerprint — encoded unambiguously (JSON array, no delimiter
injection). Two authorizations that differ in ANY of these get different ids and can never alias;
`Create` is idempotent only for a genuine re-issue of the exact same tuple, and flags any
same-id/different-identity collision. A caller-supplied authorization id is validated against the
canonical `^ark-authz-[0-9a-f]{32}$` form before it is ever used to build a filesystem path, so it
cannot cause path traversal, overwrite, or aliasing.

**Durability precisely.** Each record/marker is fsync'd AND its parent directory is fsync'd, so a
returned `CONSUMED` survives process crash and — subject to the filesystem and hardware honoring
fsync (Linux ext4/xfs do; macOS may need F_FULLFSYNC for hardware-level flush) — OS/power loss.
**What survives a process crash:** all committed transitions. **What survives OS/power loss:** the
same, to the extent the filesystem honors the fsyncs ARK issues. On-disk corruption (truncated
record, orphan/inconsistent markers, bad schema) is detected and reported `CORRUPT` — fail closed,
never re-issued. Durable mode is **fsync-bound**: throughput is ~tens–hundreds of lifecycles/sec
(not the in-memory ~100k/s) — an accepted correctness-over-throughput trade for consequential
actions. Single-winner consume relies on POSIX `O_EXCL` and a parent-directory `fsync` on a **local**
filesystem; some network filesystems do not honor `O_EXCL`, and **Windows cannot fsync a directory
handle at all** — so durable mode is supported on local **Linux/macOS only**. On Windows the durable
store is **unsupported and enforced**: `OpenFileStore` fails closed with `ErrUnsupportedPlatform`
before any I/O (never a silent in-memory fallback); general ARK supervision with in-memory
authorization remains fully supported there. The `Store` interface leaves room for a future
transactional (SQL/Redis) backend that would lift this platform restriction without changing
semantics.

## Trusted evidence plane

The deterministic gate is only as good as the facts feeding it — and if the agent's proposal and
ARK's evidence come from the *same* poisoned runtime, both are wrong in the same direction. The
trusted evidence plane separates those trust paths:

- **The agent authors the proposed action.** It never selects, replaces, edits, or downgrades the
  evidence.
- **A trusted, application-configured `EvidenceProvider` establishes the facts**, through a channel
  the agent cannot see (a system-of-record lookup, a schema/policy validator, a signed service).
  ARK — not the agent — calls it, with a *structured* request (namespace, transaction, scope,
  constraint, proposed-action shape), never a free-form agent prompt.
- **ARK deterministically checks the action against those facts.**

Every evidence object carries a **trust class**: `TRUSTED_PROVIDER` (resolved through a configured
provider), `CALLER_SUPPLIED` (handed in directly — the legacy, weaker mode), or `AGENT_SUPPLIED`.
A constraint may declare itself **protected** (`RequiresTrustedProvider`), and any check may demand
it per-call; a protected constraint **only** ALLOWs when the evidence is `TRUSTED_PROVIDER` *and*
bound to this exact request. Bindings enforced (each shortfall → `REQUIRE_EVIDENCE`, fail closed):

- **request binding** — the envelope's fingerprint must equal `RequestFingerprint(namespace,
  transaction, scope, constraint)`, so evidence resolved for one subject/transaction/policy cannot
  be reused for another;
- **subject binding** — the evidence's `subject` must match the action's `scope`;
- **namespace binding** — the evidence's tenant must match the request's;
- **freshness** — checked at both check time and pre-execution consume (the existing freshness
  machinery, with clock-skew bounds).

**Safe default (Phase 16):** `check(..., evidence=...)` is always `CALLER_SUPPLIED` — a caller can
never claim `TRUSTED_PROVIDER` through that path — so a naive `check(action, evidence=agent_output)`
can never satisfy a protected constraint. Only `check(..., provider="billing")` yields trusted
evidence. A provider that is unavailable, times out, returns malformed data, wrong subject, or
incomplete facts **fails closed** (`REQUIRE_EVIDENCE`), never ALLOW. **No LLM is used to establish
or judge evidence** — the provider is application code; the gate is deterministic.

The shipped generic `threshold` constraint demonstrates it: the agent proposes an `amount`, the
trusted provider establishes a `limit` fact, ARK checks `amount <= limit`. The agent cannot raise
its own ceiling.

The envelope also reserves an **attestation seam** (`attestation`, `issuer`, `key_id`) so a future
provider can ship signed evidence without a kernel redesign; the kernel records it and does not yet
verify it. If a deployment intentionally requires multiple sources and a deterministic policy
cannot resolve a disagreement, ARK fails closed — it never asks a model which source to believe.

## Trust boundary

The caller/integration configures the evidence provider; **ARK can separate agent-authored
proposals from evidence obtained through application-configured trust channels, and deterministically
bind authorization to that evidence — but ARK does NOT prove the facts are true.** A configured
provider can itself be wrong or compromised; external-system correctness remains a trust assumption,
provider authentication depends on the deployment, and an unwrapped tool still bypasses ARK. ARK is
responsible for recording what evidence it evaluated, validating required metadata,
enforcing configured freshness, and refusing authorization when validity or required freshness
cannot be established. It is not an OS/security boundary: an agent can bypass supervision by
invoking an unwrapped tool directly. The guarantee applies to actions routed through an
ARK-supervised path.

**Identity boundary (Phase 10):** `agent_id`, `transaction_id`, and `scope` are identifiers
supplied by the trusted integration. **ARK binds authorization to them but does not authenticate
them** — they are audit/isolation metadata, not verified identity, and `agent_id` is never an
authorization authority by itself. Authentication (a verified principal) is a future, explicitly
separate concern; the docs must not imply these ids are authenticated.

## Non-goals (explicitly out of scope)

No task/benchmark identifiers, no expected/gold values, no evaluator data, no domain constants.
No planner, no cost optimization, no action synthesis. No identity provider, policy language, or
evidence-management platform. The mechanism only *judges and gates* actions the agent authored;
it does not create them.

## Reference constraint shipped: rank-selection (generic)

A single generic constraint is included to exercise the mechanism: *"the proposed option must be
the rank-N cheapest within the complete set of retrieved, priced options."* It operates purely on
opaque option ids + numeric prices + a requested rank + an evidence-completeness flag — with no
domain values baked in. It requires `requested_rank >= 1` and an asserted-complete candidate set;
any inability to establish the rank is `REQUIRE_EVIDENCE`, never `ALLOW`.
