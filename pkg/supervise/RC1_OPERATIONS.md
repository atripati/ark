# ARK Core RC1 — operations & durability contract

The precise operational truth for running the hardened ARK supervision core. Every claim here is
backed by code + tests; nothing is aspirational.

## 1. Durable store contract (FileStore, `ARK_AUTHZ_DIR`)

**Supported substrate:** a **local POSIX filesystem** (Linux/macOS: ext4, xfs, apfs, …). The
cross-process single-winner guarantee rests on the atomicity of `open(O_CREATE|O_EXCL)` and on
`fsync` — of the file **and its parent directory** — honoring durability. It is **not supported on
network filesystems** (NFS, SMB, many object-store gateways) where `O_EXCL`/`fsync` semantics may be
weaker — do not run durable mode on those; a networked transactional `Store` implementation is a
future phase (the interface is ready). ARK does **not** attempt to auto-detect the *filesystem type*
(there is no reliable, safe probe), so the NFS/SMB exclusion is documented rather than enforced.

**Platform support (enforced):**

| platform | ARK supervision (bridge, deterministic constraints, trusted evidence, action binding, freshness, transaction isolation, **in-memory** authorization) | durable FileStore (`ARK_AUTHZ_DIR`) |
|---|---|---|
| Linux (local POSIX) | ✅ supported | ✅ supported |
| macOS (local POSIX) | ✅ supported | ✅ supported |
| **Windows** | ✅ **supported** (in-memory authorization) | ❌ **unsupported — fails closed** |

The durable FileStore is **POSIX-only** because its crash/power-loss durability requires a
**parent-directory `fsync`**, and a directory handle **cannot be fsync'd on Windows** (the call
returns `Access is denied`). This is an architectural limit, not a bug. Therefore, unlike the
filesystem-type exclusion above, the **platform** gate **is enforced**: on Windows, `OpenFileStore`
(and thus setting `ARK_AUTHZ_DIR`) **fails closed** with `ErrUnsupportedPlatform` *before any I/O* —
it never silently degrades to a weaker guarantee and never falls back to in-memory. General ARK
supervision is fully supported on Windows; only the durable backend is restricted. To run
consequential, restart-durable authorization today, use Linux or macOS on a local filesystem (a
networked transactional `Store` is the future path for other platforms/fleets).

**What survives what:**

| event | what survives | why |
|---|---|---|
| process crash / restart | ISSUED, CONSUMED, COMPLETED, retry counts | files persist; a new session reads them |
| OS crash | same, once `fsync` returned before the op was reported done | file **and parent-directory** `fsync` on every marker/record write |
| power loss | same, **if the hardware honors `fsync`** (no lying write cache) | ARK issues the syncs; disk-cache honesty is a hardware assumption |

Each state transition is an **atomic single-file** operation (`O_EXCL` marker create + `fsync` of
the file and the containing directory), so after any interruption the store is in exactly one of
{ISSUED, CONSUMED, COMPLETED} — never a partial/corrupt state. Corruption that is nonetheless
observed (truncated JSON, marker without base, completed without consumed, wrong schema) is
returned as an explicit error and **fails closed** — never interpreted as authorization.

**Never a silent downgrade:** if `ARK_AUTHZ_DIR` is set and the store cannot be opened
(unwritable/permission/incompatible schema), the session records the error and **every** check /
consume / record / status fails closed — ARK does **not** fall back to in-memory. (In-memory is
only the default when `ARK_AUTHZ_DIR` is unset; there, an authorization is lost on process exit and
an old reference fails closed as UNKNOWN.)

## 2. Reconciliation runbook — CONSUMED / AMBIGUOUS

A crash **after `consume` but before the completion is recorded** leaves an authorization in
`CONSUMED`. That is **AMBIGUOUS**: ARK claimed the authorization exactly once, but does not know
whether the external side effect actually happened. ARK never guesses and never auto-retries.

```
observe CONSUMED (status -> state=CONSUMED, reconcile note present)
        │
        ▼
query the TARGET system using the idempotency key ( == authorization_id )
        │
        ├── target confirms the operation HAPPENED
        │        └─▶ record it COMPLETED: open a session on the same ARK_AUTHZ_DIR and call
        │            record(authorization_id=<id>, executed=true, executed_action=<the action>)
        │            (ARK verifies the action fingerprint; the state becomes COMPLETED, once)
        │
        ├── target confirms the operation DID NOT happen
        │        └─▶ the authorization is spent (single-use). Do NOT "un-consume". To retry the
        │            business operation, ISSUE A NEW authorization (fresh check → consume) with
        │            fresh evidence — a new authorization_id. Leave the old one CONSUMED for audit.
        │
        └── target CANNOT determine
                 └─▶ remain AMBIGUOUS. DO NOT retry. Escalate to human/operator reconciliation.
```

- **What ARK knows:** the authorization was consumed exactly once, and the exact action/evidence it
  authorized.
- **What the external system knows:** whether the side effect occurred (only it can say).
- **What neither knows without asking:** the true external outcome after a mid-flight crash.
- **When automatic recovery is safe:** only when the target confirms COMPLETED (then record it) or
  confirms not-executed (then issue a *new* authorization). Never on "unknown".

## 3. Audit retention & rotation (RC1)

The durable audit is `audit.jsonl` in `ARK_AUTHZ_DIR`: one redacted JSON object per lifecycle
event, `fsync`'d. **RC1 has no built-in rotation** — deliberately (a rotation/retention subsystem
is not kernel work). The design supports **safe external rotation today** because the writer opens
the file per append (`O_CREATE|O_APPEND` then close each time), so:

- `mv audit.jsonl audit.jsonl.$(date +%s)` is safe under concurrent writers: an in-flight append
  lands atomically in whichever inode it opened; the next append re-creates `audit.jsonl`. **No
  record is lost or interleaved** (each line is a single `write` of a small record).
- **Lifecycle reconstruction across rotated files:** concatenate the rotated files in name order;
  each line is self-describing (`run_id`, `authorization_id`, `event`, timestamps). Proven by
  `TestFileStore_AuditSurvivesRotation`.
- **Redaction is preserved** — records are redacted *before* they are written; rotation moves bytes
  only.

Operational guidance: rotate `audit.jsonl` externally by size/age; ship rotated files to your log
store. A dedicated append-with-rotation audit sink (and optional per-line integrity hashes) is a
documented future enhancement, not an RC1 blocker.

## 4. Bridge ⇄ SDK compatibility contract

The bridge advertises `{protocol_version, capabilities}` on `hello` and on every session `start`.
The Python SDK verifies both **before trusting any supervision** and **fails loudly** on a stale or
mismatched bridge (missing protocol/capabilities, wrong protocol version, or any missing required
capability). Required capabilities: `supervision, action_binding, consume, authorization_id,
namespace, transaction_isolation, freshness, trusted_provider`. This closes the stale-bundled-bridge
risk at runtime, independent of which binary was resolved. `ARK().bridge_info()` exposes the
contract for packaging verification; the wheel fresh-install check refuses to pass on a stale
bundled bridge.

## 5. Attestation — deliberately deferred (with rationale)

The trusted-evidence envelope reserves an attestation seam (`attestation`, `issuer`, `key_id`),
**recorded but not yet verified**. RC1 does **not** implement signature verification, on purpose:

- The Python SDK and the Go bridge run in the **same process/trust domain** (the integration drives
  the bridge's stdin). A signature between them would be **fake security** — the integration could
  sign anything, so it reduces no real threat.
- Meaningful attestation requires the **provider** to be a *separate* signing authority (e.g. a
  remote signed system-of-record), with a real key-ownership and key-distribution story. That story
  is not settled for RC1, and implementing HMAC "to say signed" would be theater.

**Current, honest boundary:** ARK stamps `TRUSTED_PROVIDER` when evidence was resolved through a
provider **configured by the trusted integration**; the kernel trusts the integration to classify
truthfully (the same trust level as the code that calls ARK at all). The agent has no path to this:
the SDK forces `evidence=` to `CALLER_SUPPLIED`, and the agent never drives the bridge. Attestation
verification is the next hardening phase; the seam is in place so adding it needs no kernel
redesign.

## 6. Performance & throughput (measured, NOT a promise)

Two modes, very different cost profiles. **These are observations on one development machine, not a
guaranteed benchmark for any hardware.**

| mode | observed complete-lifecycle throughput* | restart-durable? |
|---|---|---|
| in-memory (default, `ARK_AUTHZ_DIR` unset) | ~90k–107k lifecycles/sec | **no** — state lost on process exit |
| durable FileStore (`ARK_AUTHZ_DIR` set) | **~60–130 lifecycles/sec** | yes |

\* one lifecycle = check → issue → consume → record/complete (+ audit), measured on the dev
environment.

- **Durable mode is fsync-bound**, not CPU-bound: each transition durably syncs a file *and* its
  parent directory, so per-lifecycle latency is dominated by disk `fsync` cost. Throughput scales
  with concurrency (more in-flight fsyncs overlap) but the per-store ceiling is set by the
  filesystem/hardware, not by ARK.
- **Do NOT quote the in-memory ~100k/s figure as durable throughput.** They are not comparable.
- FileStore durable mode deliberately **prioritizes correctness and durability over throughput**.
  It is intended for **consequential-action workloads** (refunds, purchases, deployments, account
  changes) whose real rate is low, **not** ultra-high-frequency or distributed serving.
- Actual numbers depend entirely on the filesystem, disk, and whether the hardware honors `fsync`;
  measure on your target hardware before sizing. A networked transactional `Store` (a future phase)
  is the path to higher-throughput/fleet serving — the FileStore is a local durable implementation.
