"""Trusted evidence providers.

An EvidenceProvider is a callable owned by the TRUSTED INTEGRATION (your application code) that
establishes facts through a channel the agent cannot see or influence — a system-of-record
lookup, a schema/policy validator, a signed service. ARK calls it (never the agent), stamps the
result as ``TRUSTED_PROVIDER`` evidence bound to the exact request, and lets a deterministic
constraint check the agent-authored action against it.

    def refund_state(request):                 # trusted application code
        cust = request["scope"]                # ARK tells the provider WHICH entity to look up
        return {"facts": {"limit": billing_db.refund_limit(cust)},
                "subject": cust, "source": "billing-db",
                "observed_at_unix": int(time.time()), "expires_at_unix": int(time.time()) + 60}

    ark = ARK(supervision="experimental", providers={"billing": refund_state})
    v = run.check(proposed_action={"kind": "refund", "fields": {"amount": amount}},
                  constraint="threshold", scope="customer-91", transaction="txn-1",
                  provider="billing")

The agent proposes the amount; ``billing`` establishes the limit; ARK checks amount <= limit.
The agent cannot choose the provider, replace it, edit its output, or downgrade to untrusted
evidence — a protected constraint refuses anything that is not TRUSTED_PROVIDER + request-bound.
"""
from __future__ import annotations

import hashlib
import time
from typing import Any, Callable, Dict, Optional

# An EvidenceProvider receives the structured request and returns a dict: recognized provenance
# keys are lifted into the envelope's meta; everything else is the evidence payload the constraint
# reads (e.g. {"facts": {...}} for threshold, {"options": [...], "requested_rank": N} for rank).
EvidenceProvider = Callable[[Dict[str, Any]], Dict[str, Any]]

_PROVENANCE_KEYS = {"subject", "source", "version", "observed_at_unix", "expires_at_unix",
                    "evidence_id", "attestation", "issuer", "key_id", "meta"}


def request_fingerprint(namespace: str, transaction: str, scope: str, constraint: str) -> str:
    """Canonical id of the QUESTION evidence must answer. Byte-for-byte identical to the Go
    kernel's supervise.RequestFingerprint (each field hex-encoded, then sha256)."""
    s = "ark-evreq-v1"
    for p in (namespace or "", transaction or "", scope or "", constraint or ""):
        s += ":" + p.encode("utf-8").hex()
    return "sha256:" + hashlib.sha256(s.encode("utf-8")).hexdigest()


def build_request(*, namespace: str, transaction: str, scope: str, constraint: str,
                  proposed: dict) -> Dict[str, Any]:
    """The structured EvidenceRequest handed to a provider. It carries only bound context — no
    free-form agent prompt — so a provider cannot be confused into answering the wrong question."""
    return {
        "namespace": namespace or "", "transaction": transaction or "", "scope": scope or "",
        "constraint": constraint, "proposed": proposed,
        "request_fingerprint": request_fingerprint(namespace, transaction, scope, constraint),
    }


def envelope_from_provider(provider_id: str, request: Dict[str, Any],
                           result: Dict[str, Any]) -> Dict[str, Any]:
    """Wrap a provider's returned facts into a trusted, request-bound evidence envelope.

    The trust class, provider id, namespace, and request fingerprint are set by ARK (not the
    provider or the agent). The provider supplies the facts and optional provenance/freshness.
    """
    if not isinstance(result, dict):
        raise TypeError(f"evidence provider {provider_id!r} must return a dict, got {type(result).__name__}")
    payload = {k: v for k, v in result.items() if k not in _PROVENANCE_KEYS}
    payload["meta"] = {
        "trust": "TRUSTED_PROVIDER",
        "provider_id": provider_id,
        "subject": result.get("subject", request["scope"]),
        "namespace": request["namespace"],
        "request_fingerprint": request["request_fingerprint"],
        "source": result.get("source", provider_id),
        "version": result.get("version", ""),
        "observed_at_unix": int(result.get("observed_at_unix", time.time())),
        "expires_at_unix": int(result.get("expires_at_unix", 0)),
        "evidence_id": result.get("evidence_id", ""),
        "attestation": result.get("attestation", ""),
        "issuer": result.get("issuer", ""),
        "key_id": result.get("key_id", ""),
    }
    return payload


def stamp_caller_supplied(evidence: Optional[dict]) -> dict:
    """Force the trust class of directly-supplied evidence to CALLER_SUPPLIED. A caller can NEVER
    claim TRUSTED_PROVIDER through the ``evidence=`` path — only a configured provider yields that,
    so a protected constraint can never be satisfied by agent- or caller-supplied evidence."""
    ev = dict(evidence or {})
    meta = dict(ev.get("meta") or {})
    meta["trust"] = "CALLER_SUPPLIED"
    ev["meta"] = meta
    return ev
