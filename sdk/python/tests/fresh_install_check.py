"""Checks a fresh wheel install actually works. Run from a venv that only has ark-runtime
installed, from a dir outside the repo, with no PYTHONPATH and no ARK_BRIDGE_BIN.

    python fresh_install_check.py

Fails (nonzero exit) if anything is off. Doesn't print secrets.
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

    # import the base SDK
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

    # run a task (mock mode, no api key needed)
    print("\nARK().run (mock):")
    r = ARK().run(task="find the top Python web frameworks on GitHub")
    check("bundled bridge started and returned a RunResult", r is not None)
    check("run succeeded", r.success is True)
    check("decision telemetry present", len(r.decisions) >= 1 and bool(r.decisions[0].model))
    total = sum(d.cost.total_cost for d in r.decisions)
    check("cost reconciles (sum decisions == total_cost)", math.isclose(total, r.total_cost, abs_tol=1e-9))
    check("cost_by_model reconciles", math.isclose(sum(r.cost_by_model.values()), r.total_cost, abs_tol=1e-9))
    print(f"    run_id={r.run_id} decisions={len(r.decisions)} total_cost={r.total_cost}")

    # the trace API
    print("\nark.trace (external-agent API):")
    with ark.trace("test") as run:
        run.record(action="tool_call", model="gpt-4o-mini", tool="search",
                   input_tokens=100, output_tokens=20, outcome="success")
        run.record(action="complete", model="gpt-4o", input_tokens=200, output_tokens=40, outcome="stop")
    tr = run.result
    check("trace produced a canonical RunResult", tr.success is True and len(tr.decisions) == 2)
    check("trace cost reconciles",
          math.isclose(sum(d.cost.total_cost for d in tr.decisions), tr.total_cost, abs_tol=1e-9))

    # supervision should be off by default
    print("\nsupervision default:")
    from ark import ArkSupervisionDisabled
    off = False
    try:
        ARK().supervise(constraint="rank", proposed={"option": "A"}, evidence={})
    except ArkSupervisionDisabled:
        off = True
    check("experimental supervision is OFF by default", off)

    # langgraph extra (optional)
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
