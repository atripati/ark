"""Fresh-install acceptance check — run from a venv that has ONLY the installed ark-runtime
wheel, from a directory OUTSIDE the repo, with no PYTHONPATH and no ARK_BRIDGE_BIN.

    python tests/fresh_install_check.py     # (path is just where this file happens to live)

Proves the whole out-of-the-box experience: package imports, bundled bridge auto-discovered
and started, Go runtime responds, canonical RunResult returns, telemetry + cost reconcile,
ark.trace works, supervision is OFF by default, and the LangGraph extra behaves. Prints a
report and exits non-zero on any failure. Never prints secrets.
"""
import math
import os
import sys


def check(label, cond):
    print(f"  [{'PASS' if cond else 'FAIL'}] {label}")
    if not cond:
        raise SystemExit(f"fresh-install check failed: {label}")


def main():
    print("environment:")
    print("  cwd:", os.getcwd())
    print("  PYTHONPATH:", os.environ.get("PYTHONPATH", "<unset>"))
    print("  ARK_BRIDGE_BIN:", os.environ.get("ARK_BRIDGE_BIN", "<unset>"))
    check("cwd is outside a source checkout (no ./ark package)",
          not os.path.isdir(os.path.join(os.getcwd(), "ark")))

    # 1. package import (base SDK, no LangGraph)
    import ark
    from ark import ARK
    from ark.bridge import _bundled_binary, _find_binary
    print("\npackage:")
    print("  ark version:", ark.__version__)
    print("  ark loaded from:", os.path.dirname(ark.__file__))
    check("ark is imported from site-packages (installed wheel), not the repo",
          "site-packages" in ark.__file__ or "dist-packages" in ark.__file__)
    check("bundled bridge discovered inside the package", _bundled_binary() is not None)
    check("_find_binary resolves to the bundled bridge with no override",
          "_bridge" in _find_binary())

    # 2. normal ark.run through the bundled bridge (mock mode — no external credentials)
    print("\nARK().run (mock):")
    r = ARK().run(task="find the top Python web frameworks on GitHub")
    check("bundled bridge started and returned a RunResult", r is not None)
    check("run succeeded", r.success is True)
    check("decision telemetry present", len(r.decisions) >= 1 and bool(r.decisions[0].model))
    total = sum(d.cost.total_cost for d in r.decisions)
    check("cost reconciles (Σ decisions == total_cost)", math.isclose(total, r.total_cost, abs_tol=1e-9))
    check("cost_by_model reconciles", math.isclose(sum(r.cost_by_model.values()), r.total_cost, abs_tol=1e-9))
    print(f"    run_id={r.run_id} decisions={len(r.decisions)} total_cost={r.total_cost}")

    # 3. external-agent trace API
    print("\nark.trace (external-agent API):")
    with ark.trace("test") as run:
        run.record(action="tool_call", model="gpt-4o-mini", tool="search",
                   input_tokens=100, output_tokens=20, outcome="success")
        run.record(action="complete", model="gpt-4o", input_tokens=200, output_tokens=40, outcome="stop")
    tr = run.result
    check("trace produced a canonical RunResult", tr.success is True and len(tr.decisions) == 2)
    check("trace cost reconciles",
          math.isclose(sum(d.cost.total_cost for d in tr.decisions), tr.total_cost, abs_tol=1e-9))

    # 4. supervision OFF by default
    print("\nsupervision default:")
    from ark import ArkSupervisionDisabled
    off = False
    try:
        ARK().supervise(constraint="rank", proposed={"option": "A"}, evidence={})
    except ArkSupervisionDisabled:
        off = True
    check("experimental supervision is OFF by default", off)

    # 5. LangGraph optional extra behavior
    print("\nLangGraph extra:")
    try:
        import langgraph  # noqa: F401
        have_lg = True
    except Exception:
        have_lg = False
    if have_lg:
        from ark.integrations.langgraph import ArkCallbackHandler  # noqa: F401
        check("with [langgraph] extra installed, the adapter imports", True)
    else:
        got_msg = False
        try:
            import ark.integrations.langgraph  # noqa: F401
        except ImportError as e:
            got_msg = "pip install" in str(e) and "langgraph" in str(e)
        check("without the extra, the integration raises a clear install message", got_msg)

    print("\nALL FRESH-INSTALL CHECKS PASSED")


if __name__ == "__main__":
    sys.exit(main())
