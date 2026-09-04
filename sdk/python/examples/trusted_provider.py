"""Trusted evidence plane — the agent proposes, the application establishes facts, ARK checks.

Run:  PYTHONPATH=sdk/python python sdk/python/examples/trusted_provider.py

Shows two reference providers, both owned by TRUSTED APPLICATION CODE (never the agent):
  1. a system-of-record lookup (refund limit) for the generic `threshold` constraint;
  2. a validator-style provider (deployment policy) — the same pattern for app/schema validation.
"""
import time
from ark import ARK

# --- the TRUSTED systems of record (the agent cannot see or influence these) ---
BILLING = {"customer-91": 500.0}          # real refund limits
DEPLOYS = {"web@1.4.2": {"tests_passed": True, "window_open": True}}


def refund_limit_provider(request):
    """System-of-record lookup. ARK tells the provider WHICH entity to look up (request['scope'])."""
    customer = request["scope"]
    return {"facts": {"limit": BILLING.get(customer, 0.0)},
            "subject": customer, "source": "billing-db", "version": "v1",
            "observed_at_unix": int(time.time()), "expires_at_unix": int(time.time()) + 60}


def deploy_policy_provider(request):
    """Validator-style provider: trusted app code checks the real deployment state. It returns a
    numeric 'limit' of 1 iff the deploy is allowed, else 0 (the generic threshold expresses the
    yes/no as amount<=limit; a domain deployment constraint could read richer facts)."""
    target = request["proposed"]["fields"]["target"]          # e.g. "web@1.4.2"
    d = DEPLOYS.get(target, {})
    allowed = 1.0 if (d.get("tests_passed") and d.get("window_open")) else 0.0
    return {"facts": {"limit": allowed}, "subject": request["scope"], "source": "deploy-controller"}


ark = ARK(supervision="experimental",
          providers={"billing": refund_limit_provider, "deploys": deploy_policy_provider})

with ark.trace("refund + deploy under trusted evidence") as run:
    # The agent's context was poisoned to believe the refund limit is $5,000; it proposes $4,000.
    v = run.check(proposed_action={"kind": "refund", "fields": {"amount": 4000}},
                  constraint="threshold", scope="customer-91", transaction="refund-1",
                  agent_id="agent-27", provider="billing")
    print(f"refund $4000 vs trusted $500 -> {v.verdict}  ({v.reason})")

    # A legitimate $100 refund clears the pre-execution gate and executes once.
    act = {"kind": "refund", "fields": {"amount": 100}}
    v2 = run.check(act, constraint="threshold", scope="customer-91", transaction="refund-2", provider="billing")
    if v2.allowed and run.consume(v2, executed_action=act).cleared:
        run.record(action="tool_call", tool="issue_refund", of=v2, executed=True, executed_action=act,
                   model="gpt-4o", input_tokens=5, output_tokens=1)
        print(f"refund $100 -> {v2.verdict}, executed once (idem key {v2.idempotency_key[:24]}...)")

    # Deployment: allowed target vs a target with no passing tests.
    ok = run.check({"kind": "deploy", "fields": {"amount": 1, "target": "web@1.4.2"}},
                   constraint="threshold", scope="web-prod", transaction="dep-1", provider="deploys")
    bad = run.check({"kind": "deploy", "fields": {"amount": 1, "target": "web@9.9.9"}},
                    constraint="threshold", scope="web-prod", transaction="dep-2", provider="deploys")
    print(f"deploy web@1.4.2 -> {ok.verdict}   deploy web@9.9.9 (no tests) -> {bad.verdict}")

print("\nkey point: the agent proposed every amount/target; the TRUSTED providers set the facts;")
print("ARK checked deterministically. The agent could not raise its own ceiling or forge evidence.")
