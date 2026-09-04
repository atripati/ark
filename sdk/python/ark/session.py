"""External-agent integration: ``with ark.trace(...) as run:``.

The developer keeps their own agent/model/tools and drives their own loop. Inside the
trace they REPORT what happened (``run.record(...)``) and, optionally, ask ARK to gate a
proposed action before executing it (``run.check(...)``). On exit ARK returns the canonical
``RunResult`` — the same contract ``ARK.run`` produces.

This module is a THIN line-protocol client over a persistent ``ark-bridge --session``
process. All session state that ARK owns — retry counters, verdict semantics, recovery
logic, stable decision IDs, cost pricing, aggregate derivation — lives in Go, so Python
never reimplements or drifts from it. Fields ARK cannot observe (model, tokens, tool,
latency, verification, routing) are reported by the developer and are marked as such in
``run.provenance``; they are never presented as if ARK observed them directly.
"""
from __future__ import annotations

import json
import queue
import subprocess
import sys
import threading
from typing import Any, Optional

from .bridge import _find_binary
from .errors import ArkError, ArkBridgeError, ArkSupervisionDisabled, ArkSupervisionError, ArkProviderError
from .models import RunResult
from . import providers as _providers

# Bridge<->SDK compatibility contract. The SDK refuses to run against a bridge that does not
# advertise a compatible protocol AND every capability whose guarantee the SDK relies on — so a
# stale/mismatched bridge fails LOUDLY instead of silently degrading supervision.
PROTOCOL_VERSION = 1
REQUIRED_CAPABILITIES = frozenset({
    "supervision", "action_binding", "consume", "authorization_id",
    "namespace", "transaction_isolation", "freshness", "trusted_provider",
})


def check_bridge_compat(reply: dict) -> None:
    """Raise ArkBridgeError unless the bridge advertises a compatible protocol + capabilities."""
    pv = reply.get("protocol_version")
    caps = set(reply.get("capabilities") or [])
    if pv is None or not caps:
        raise ArkBridgeError(
            "ark-bridge did not advertise a protocol version/capabilities — it is STALE or "
            "incompatible with this SDK. An installed `ark-agent-runtime` wheel bundles a matching "
            "bridge; in a source checkout, build a fresh one and set ARK_BRIDGE_BIN.")
    if pv != PROTOCOL_VERSION:
        raise ArkBridgeError(
            f"ark-bridge protocol version {pv} is incompatible with this SDK (expects "
            f"{PROTOCOL_VERSION}); reinstall/rebuild a matching bridge.")
    missing = REQUIRED_CAPABILITIES - caps
    if missing:
        raise ArkBridgeError(
            "ark-bridge is missing required capabilities: " + ", ".join(sorted(missing)) +
            ". It is stale/incompatible and supervision would be weaker than this SDK assumes — "
            "refusing to run. Rebuild/reinstall the bridge.")


class Verdict:
    """One supervision verdict, and the stable handle for the decision it created.

    Pass it back as ``run.record(..., of=verdict)`` so the executed telemetry lands on the
    SAME DecisionRecord as the verdict — an unambiguous proposal -> execution audit chain.
    ``suggested`` is runtime-derived EVIDENCE (e.g. the rank-2 option id); ARK never authors
    the replacement action — your agent re-proposes.
    """

    def __init__(self, d: dict):
        self.decision_id: Optional[str] = d.get("decision_id")
        self.verdict: Optional[str] = d.get("verdict")
        self.reason: Optional[str] = d.get("reason")
        self.retry_number: Optional[int] = d.get("retry_number")
        self.suggested: Optional[str] = d.get("suggested") or None
        self.allowed: bool = bool(d.get("allowed"))
        self.scope: Optional[str] = d.get("scope") or None
        self.transaction_id: Optional[str] = d.get("transaction_id") or None
        # the stable, durable authorization id — usable to consume/record across restart and
        # across ARK instances. Also serves as the idempotency key a cooperating external API can
        # honour to dedupe the real side effect (ARK does not own that side effect).
        self.authorization_id: Optional[str] = d.get("authorization_id") or None
        self.idempotency_key: Optional[str] = d.get("idempotency_key") or d.get("authorization_id") or None
        # fingerprints binding this verdict to the exact action + evidence it evaluated.
        self.action_fingerprint: Optional[str] = d.get("action_fingerprint") or None
        self.evidence_fingerprint: Optional[str] = d.get("evidence_fingerprint") or None

    def __repr__(self) -> str:
        return (f"Verdict(verdict={self.verdict!r}, allowed={self.allowed}, "
                f"suggested={self.suggested!r}, txn={self.transaction_id!r}, id={self.decision_id!r})")


class Cleared:
    """Result of the PRE-EXECUTION :meth:`RunSession.consume` gate. Execute the side effect ONLY
    when ``cleared`` is True. When False, the authorization went stale before use (re-check with
    fresh evidence). ``idempotency_key`` may be forwarded to a cooperating external API."""

    def __init__(self, d: dict):
        self.cleared: bool = bool(d.get("cleared"))
        self.decision_id: Optional[str] = d.get("decision_id")
        self.idempotency_key: Optional[str] = d.get("idempotency_key")
        self.requires_recheck: bool = bool(d.get("requires_recheck"))
        self.reason: Optional[str] = d.get("reason")

    def __repr__(self) -> str:
        return f"Cleared(cleared={self.cleared}, id={self.decision_id!r}, reason={self.reason!r})"


class _SessionProc:
    """A persistent ``ark-bridge --session`` subprocess speaking one JSON object per line.

    The transport is an implementation detail behind ``RunSession`` — it can be swapped for a
    local service later without changing the public API. It carries NO session logic: it just
    forwards a command dict and returns the parsed response dict.
    """

    def __init__(self, binary: Optional[str] = None, timeout: int = 120):
        self._bin = binary or _find_binary()
        self._timeout = timeout
        self._p: Optional[subprocess.Popen] = None
        # one line-protocol pipe, so the whole write/read transaction must be atomic. LangGraph
        # runs tools (and thus check/record) from a thread pool; without this lock concurrent
        # callers splice each other's bytes and corrupt the JSON.
        self._io_lock = threading.Lock()
        # A background reader turns the blocking readline() into a queue we can wait on WITH a
        # timeout, cross-platform (no select, which does not work on Windows pipes). Responses
        # arrive in request order because sends are serialized by _io_lock.
        self._responses: "queue.Queue[bytes]" = queue.Queue()
        self._reader: Optional[threading.Thread] = None
        self._dead = False  # set once the session is unusable (timeout / EOF / write error)

    def start(self) -> None:
        self._p = subprocess.Popen(
            [self._bin, "--session"],
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, bufsize=0,
        )
        self._dead = False
        self._responses = queue.Queue()
        self._reader = threading.Thread(target=self._read_loop, args=(self._p,), daemon=True)
        self._reader.start()

    def _read_loop(self, proc: subprocess.Popen) -> None:
        try:
            for line in iter(proc.stdout.readline, b""):
                self._responses.put(line)
        except Exception:
            pass
        finally:
            self._responses.put(b"")  # EOF sentinel: the reader has stopped

    def send(self, cmd: dict) -> dict:
        payload = (json.dumps(cmd) + "\n").encode()
        # Serialize the ENTIRE request/response: one request goes out and exactly one response
        # line is taken before any other thread may proceed. This guarantees protocol integrity
        # for concurrent callers; it adds no parallel-action semantics. Go stays authoritative.
        with self._io_lock:
            if self._p is None or self._dead or self._p.poll() is not None:
                raise ArkBridgeError("session process is not running")
            try:
                self._p.stdin.write(payload)
                self._p.stdin.flush()
            except (OSError, BrokenPipeError) as e:
                self._dead = True
                raise ArkBridgeError(f"session transport failed: {e}") from e
            try:
                out = self._responses.get(timeout=self._timeout)
            except queue.Empty:
                # the bridge did not answer in time. Kill it and mark the session unusable, so a
                # late/stale response can never be read as the answer to a later command.
                self._dead = True
                self._kill()
                raise ArkBridgeError(
                    f"session timed out after {self._timeout}s waiting for a response; the "
                    f"bridge was terminated and this session is now unusable")
            if not out:  # EOF sentinel — the bridge exited / closed stdout
                self._dead = True
                err = self._drain_stderr()
                raise ArkBridgeError("session ended without a response" + (f": {err}" if err else ""))
        try:
            data = json.loads(out.decode())
        except json.JSONDecodeError as e:
            raise ArkBridgeError(f"session returned non-JSON: {out[:200]!r}") from e
        if isinstance(data, dict) and data.get("error"):
            raise ArkBridgeError(data["error"])
        return data

    def _kill(self) -> None:
        try:
            if self._p is not None:
                self._p.kill()
        except Exception:
            pass

    def _drain_stderr(self) -> str:
        # only called on the EOF path, where the process has exited, so read() will not block.
        try:
            return (self._p.stderr.read() or b"")[:300].decode(errors="replace") if self._p else ""
        except Exception:
            return ""

    def close(self) -> None:
        # take the same lock so we never close stdin mid-transaction. It does not nest inside
        # send() (finish() sends, then __exit__ calls close()), so it cannot deadlock; a
        # concurrent in-flight send completes (or times out) first. Safe after a timeout too:
        # _p may already be killed, in which case the teardown below is a no-op that still
        # clears state and joins the reader.
        with self._io_lock:
            p, self._p = self._p, None
            reader, self._reader = self._reader, None
            self._dead = True
            if p is not None:
                try:
                    if p.stdin and not p.stdin.closed:
                        p.stdin.close()
                    p.wait(timeout=5)
                except Exception:
                    try:
                        p.kill()
                    except Exception:
                        pass
        if reader is not None:
            reader.join(timeout=2)  # unblocks on EOF once the process exits


class RunSession:
    """A single ARK trace around an external agent's run. Use as a context manager.

    ``supervision="experimental"`` is required to call ``check``; observe-only traces omit it.
    """

    def __init__(self, task: str, *, task_type: Optional[str] = None, supervision: str = "off",
                 provider: str = "openai", budget: int = 4, run_id: Optional[str] = None,
                 transport: Optional[_SessionProc] = None, providers: Optional[dict] = None):
        if supervision not in ("off", "experimental"):
            raise ValueError("supervision must be 'off' or 'experimental'")
        self.task = task
        self.run_id: Optional[str] = run_id
        self._task_type = task_type
        self._supervision = supervision
        self._provider = provider
        self._budget = budget
        # trusted evidence providers, registered by the application (never the agent). Keyed by name.
        self._providers: dict = dict(providers or {})
        self._proc = transport or _SessionProc()
        self._result: Optional[RunResult] = None
        self._provenance: Optional[dict] = None
        self._finished = False
        self._started = False

    # -- lifecycle -----------------------------------------------------------------
    def __enter__(self) -> "RunSession":
        self._proc.start()
        r = self._proc.send({
            "cmd": "start", "task": self.task, "task_type": self._task_type or "",
            "supervision": self._supervision, "provider": self._provider,
            "budget": self._budget, "run_id": self.run_id or "",
        })
        try:  # refuse a stale/incompatible bridge BEFORE any supervision is trusted
            check_bridge_compat(r)
        except ArkBridgeError:
            self._proc.close()
            raise
        self.run_id = r.get("run_id")
        self._started = True
        return self

    def __exit__(self, exc_type, exc, tb) -> bool:
        try:
            if self._started and not self._finished:
                self.finish(
                    success=exc_type is None,
                    termination_reason=None if exc_type is None else f"exception: {exc_type.__name__}",
                )
        finally:
            self._proc.close()
        return False  # never suppress the developer's exception

    # -- supervision gate ----------------------------------------------------------
    def check(self, proposed_action: Any, constraint: str, evidence: dict = None, *,
              scope: str, transaction: Optional[str] = None, agent_id: Optional[str] = None,
              namespace: Optional[str] = None, provider: Optional[str] = None,
              require_trusted_provider: Optional[bool] = None,
              action: str = "tool_call", tool: Optional[str] = None,
              max_evidence_age: Optional[int] = None) -> Verdict:
        """Gate a proposed action BEFORE executing it. Synchronous, non-authoring.

        ``scope`` (REQUIRED) is the resource/entity affected (e.g. an order/customer id).
        ``transaction`` identifies ONE authorization lifecycle; retry budgets are isolated per
        ``(transaction, constraint)`` so two separate transactions for the same entity never
        share a budget. It defaults to ``scope`` — pass a distinct ``transaction`` per
        consequential workflow when one entity has several. ``agent_id`` is optional audit
        metadata naming the logical agent making the proposal.

        ARK FAILS CLOSED: an unknown constraint, a missing scope, or malformed/typo'd evidence
        raises rather than returning ALLOW. Returns a :class:`Verdict`. On ALLOW, call
        :meth:`consume` immediately before the side effect (it re-checks freshness/replay at use
        time); ARK never authors the next action. Optionally set ``max_evidence_age`` (seconds)
        to require the evidence's ``meta.observed_at_unix`` to be recent, else REQUIRE_EVIDENCE.
        """
        if not self._started:
            raise ArkError("check() outside an open trace")
        if self._supervision != "experimental":
            raise ArkSupervisionDisabled(
                "constrained supervision is experimental and off by default; "
                "open the trace with supervision='experimental'")
        if not isinstance(scope, str) or not scope.strip():
            raise ArkSupervisionError(
                "check() requires a non-empty 'scope' identifying the resource/entity being "
                "supervised")
        proposed = _as_proposed(proposed_action)
        require = bool(require_trusted_provider)

        if provider is not None:
            # TRUSTED path: resolve evidence through the application-configured provider (the agent
            # cannot select or influence it). A provider that raises fails CLOSED to REQUIRE_EVIDENCE
            # rather than proceeding.
            fn = self._providers.get(provider)
            if fn is None or not callable(fn):
                raise ArkProviderError(
                    f"evidence provider {provider!r} is not registered; register it on ARK(providers=...)")
            req = _providers.build_request(namespace=namespace or "", transaction=transaction or "",
                                           scope=scope, constraint=constraint, proposed=proposed)
            try:
                result = fn(req)
                ev = _providers.envelope_from_provider(provider, req, result)
            except Exception as exc:  # provider unavailable/timeout/malformed -> fail closed
                return Verdict({"verdict": "REQUIRE_EVIDENCE", "allowed": False, "requires_recheck": True,
                                "reason": f"evidence provider {provider!r} failed ({type(exc).__name__}: {exc}); "
                                          f"no trusted evidence, refusing"})
            require = True  # using a provider implies the protected path
        else:
            # LEGACY path: directly-supplied evidence is always CALLER_SUPPLIED (never trusted), so
            # it can never satisfy a protected constraint.
            ev = _providers.stamp_caller_supplied(evidence)

        r = self._proc.send({
            "cmd": "check", "action": action, "tool": tool or "",
            "constraint": constraint, "scope": scope, "namespace": namespace or "",
            "transaction_id": transaction or "", "agent_id": agent_id or "",
            "proposed": proposed, "evidence": ev,
            "max_evidence_age_sec": int(max_evidence_age) if max_evidence_age else 0,
            "require_trusted_provider": require,
        })
        return Verdict(r)

    def consume(self, of: "Verdict | str", executed_action: Any) -> Cleared:
        """PRE-EXECUTION gate. Call IMMEDIATELY BEFORE the real side effect, passing the actual
        structured action you are about to execute. ARK re-validates the authorization against a
        fresh clock (freshness window still open, verdict still ALLOW, action still matches, not
        already consumed) and consumes it exactly once. Execute ONLY when the returned
        :class:`Cleared` has ``cleared is True``; when False the authorization went stale — do
        NOT execute, re-check with fresh evidence. Hard problems (non-ALLOW, action mismatch,
        replay, missing action) raise. This is the strong TOCTOU guarantee; a bare check->record
        still enforces action binding and single-execution but skips this pre-execution re-check.
        """
        if not self._started:
            raise ArkError("consume() outside an open trace")
        of_id, auth_id = _authrefs(of)
        r = self._proc.send({
            "cmd": "consume", "of": of_id, "authorization_id": auth_id,
            "executed_action": _as_proposed(executed_action) if executed_action is not None else None,
        })
        return Cleared(r)

    # -- telemetry report ----------------------------------------------------------
    def record(self, *, action: Optional[str] = None, model: Optional[str] = None,
               tool: Optional[str] = None, tool_args: Optional[dict] = None,
               input_tokens: Optional[int] = None, output_tokens: Optional[int] = None,
               cost: Optional[float] = None, latency_ms: Optional[int] = None,
               routing_reason: Optional[str] = None, verification: Optional[dict] = None,
               outcome: Optional[str] = None, error: Optional[str] = None,
               executed: bool = True, of: "Verdict | str | None" = None,
               executed_action: Any = None) -> Optional[str]:
        """Report one decision's telemetry. With ``of=<verdict>`` it completes the decision
        that ``check`` created; otherwise it records a fresh (unsupervised) decision.

        When you confirm execution of an ARK authorization (``of=<ALLOW verdict>`` with
        ``executed=True``), you MUST pass ``executed_action`` — the actual structured action you
        executed (a bare id, ``{"option": ...}``, or a full ProposedAction dict). ARK
        canonicalizes it and requires its fingerprint to match the action that received ALLOW;
        a missing or different action is refused (fail closed). You cannot confirm execution by
        echoing a fingerprint — you must present the real action.

        Everything here is REPORTED by your runtime — ARK derives only ids/ordering and, when
        you give tokens+model but no ``cost``, the cost (via ARK's pricing tables). Pass
        ``error`` for a real failure: it populates the canonical DecisionRecord.error field
        (aggregated into RunResult.errors); ``outcome`` is kept separately for compatibility.
        """
        if not self._started:
            raise ArkError("record() outside an open trace")
        of_id, auth_id = _authrefs(of)
        r = self._proc.send({
            "cmd": "record", "action": action or "", "model": model or "",
            "tool": tool or "", "tool_args": tool_args,
            "input_tokens": input_tokens or 0, "output_tokens": output_tokens or 0,
            "cost": cost, "latency_ms": latency_ms or 0,
            "routing_reason": routing_reason or "", "verification": verification,
            "outcome": outcome or "", "error": error or "",
            "executed": executed, "of": of_id, "authorization_id": auth_id,
            "executed_action": _as_proposed(executed_action) if executed_action is not None else None,
        })
        return r.get("decision_id")

    def finish(self, *, success: bool = True, termination_reason: Optional[str] = None,
               output: Optional[str] = None) -> RunResult:
        """End the trace and return the canonical RunResult. Called automatically on exit."""
        if self._finished:
            return self._result  # type: ignore[return-value]
        r = self._proc.send({
            "cmd": "finish", "success": success,
            "termination_reason": termination_reason or "", "output": output or "",
        })
        self._result = RunResult.from_dict(r["run_result"])
        self._provenance = r.get("provenance")
        self._finished = True
        return self._result

    # -- results -------------------------------------------------------------------
    @property
    def result(self) -> RunResult:
        if self._result is None:
            raise ArkError("run result is not available until the trace finishes")
        return self._result

    @property
    def provenance(self) -> Optional[dict]:
        """Per-decision {reported:[...], derived:[...]} split — which facts came from your
        runtime vs which ARK generated. Available after the trace finishes."""
        return self._provenance

    def report(self, verbose: bool = False, file=None) -> None:
        """Print a human-readable report of this run. Quiet by default: nothing is printed
        unless you call this. Built entirely from the canonical RunResult (no parallel schema,
        no invented fields); reported vs ARK-derived facts stay distinguishable, and in verbose
        mode the per-decision provenance is shown. Available after the trace finishes."""
        from .report import format_run
        print(format_run(self.result, verbose=verbose, provenance=self._provenance),
              file=file or sys.stdout)


def _authrefs(of: "Verdict | str | None") -> "tuple[str, str]":
    """Resolve an authorization reference into (in-session decision id, stable authorization id).

    Accepts a :class:`Verdict` (carries both — works in-session and across restart/instances), a
    stable authorization id string (``ark-authz-...`` — works across processes), or a plain
    decision id string (in-session only)."""
    if isinstance(of, Verdict):
        return (of.decision_id or ""), (of.authorization_id or "")
    if isinstance(of, str):
        if of.startswith("ark-authz-"):
            return "", of
        return of, ""
    return "", ""


def _as_proposed(p: Any) -> dict:
    """Accept a bare option id, ``{"option": ...}``, or a full ProposedAction-shaped dict."""
    if isinstance(p, dict):
        if "option" in p or "kind" in p or "fields" in p:
            return p
        return {"fields": p}
    return {"option": str(p)}
