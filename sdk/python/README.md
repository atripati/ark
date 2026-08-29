# ARK Runtime SDK (`ark-runtime`)

Attach ARK's decision telemetry, cost attribution, and (experimental) constrained supervision
around your existing agent — you keep your model, your tools, and your runtime.

```bash
pip install ark-runtime
```

```python
from ark import ARK

# run a task through the ARK runtime and get the canonical RunResult
result = ARK().run(task="find the top Python web frameworks on GitHub")
print(result.success, result.total_cost, [d.model for d in result.decisions])
```

The wheel bundles the ARK runtime bridge for your platform — **no Go toolchain, no build step,
no `ARK_BRIDGE_BIN`.** `import ark` needs no other dependencies.

## Attach ARK around your own agent

```python
import ark

with ark.trace("book the 2nd-cheapest flight") as run:
    # ... your agent loop; you own the model and tools ...
    run.record(action="tool_call", model="gpt-4o", tool="search",
               input_tokens=449, output_tokens=16, outcome="success")
result = run.result          # canonical RunResult (same schema as ARK.run)
```

Experimental constrained supervision is **off by default** and opt-in:

```python
ark.ARK(supervision="experimental")
```

## LangGraph integration (optional)

```bash
pip install "ark-runtime[langgraph]"
```

```python
from ark.integrations.langgraph import ArkCallbackHandler, ark_supervise_tool, build_agent
```

Without the extra, `import ark` still works; importing the integration prints a clear install
message.

## What you get

- Canonical `RunResult` / `DecisionRecord` telemetry (decision-level).
- Decision-level cost attribution (routing-aware, deterministic model pricing).
- Routing, tool activity, verification (where available), and errors.
- Experimental constrained supervision (ALLOW / REJECT / REQUIRE_EVIDENCE / RECOVERY_EXHAUSTED).

The SDK does not reimplement ARK; it is a thin client over the bundled ARK runtime and makes
no unproven claims.
