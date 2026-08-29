# LangGraph integration — interface audit + adapter design

Audited against **langgraph 1.2.x / langchain-core 1.6.x** by introspecting the installed
library and running real agents (`create_react_agent`) with a scripted fake model — not from
memory. The adapter is thin:

```
LangGraph lifecycle/events  →  ARK generic trace/check/record  →  Go ARK supervision + telemetry
```

It reimplements no routing, supervision, retries, pricing, or telemetry — every field is
either reported from a LangChain event or derived by Go ARK behind the frozen `ark.trace()`
session.

## Where the signals are (verified)

| question | answer (verified) |
|---|---|
| where model calls begin/end | `BaseCallbackHandler.on_chat_model_start` / `on_llm_start` → `on_llm_end(response: LLMResult)` |
| where tool calls are proposed | in the model output at `on_llm_end`: `generations[0][0].message.tool_calls` |
| clean pre-tool-execution interception? | **not in the callback handler** — handlers are observational; `on_tool_start` cannot cleanly veto. The framework-native gate is the **tool callable itself** (wrap it), plus `create_react_agent`'s `post_model_hook` / `interrupt_before=["tools"]`. |
| where token usage / model names are exposed | `on_llm_end`: `message.usage_metadata{input_tokens, output_tokens}` (modern) or `response.llm_output["token_usage"]` (legacy); model from `message.response_metadata["model_name"]` |
| where tool outcomes / errors are exposed | `on_tool_end(output)` / `on_tool_error(error)` (name+args captured at `on_tool_start`, incl. the `inputs` dict) |

## Automatic vs. developer-supplied

**Observed automatically by the adapter** (from LangChain callbacks, no developer help):
- model calls (start/end), model name, input/output tokens → **ARK derives cost**;
- tool calls: name, args, outcome/error, latency.

**Still requires the developer** (ARK cannot infer it from the graph):
- *which* tool to supervise, the applicable **constraint**, and the **trusted runtime
  evidence** (e.g. the retrieved, priced options) — all passed to `ark_supervise_tool(...)`.

## The two mechanisms

- **`ArkCallbackHandler(run, record_tools=True)`** — a `BaseCallbackHandler`; attach with
  `config={"callbacks": [handler]}`. Maps `on_llm_end` → a cost-bearing model decision and
  `on_tool_start/end` → a tool-activity decision. Observe-only; it never blocks the agent.
- **`ark_supervise_tool(run, tool, constraint=, evidence=)`** — wraps a tool so ARK gates the
  proposed call *before* the real logic runs. On ALLOW the tool executes and the telemetry
  lands on the verdict's decision (`of=verdict`); on a non-ALLOW verdict the tool does **not**
  run and ARK's runtime-derived suggestion is returned as the tool result — which LangGraph's
  own loop feeds back to the model to **replan**. No faked veto. Pair it with the handler in
  `record_tools=False` mode so tool decisions aren't double-counted.

A supervised turn therefore surfaces as: model decision (cost) → tool decision `REJECT`
(not executed, zero cost) → model decision (cost) → tool decision `ALLOW` (executed) → final
model decision — the full proposal → supervision → retry → execution → outcome → cost chain
in one canonical `RunResult`.

## Scope / limits

Sync graphs (`.invoke(...)`); async/parallel tool fan-out is out of scope. The core SDK has
no dependency on LangGraph — the integration is imported lazily and installed via the extra
`pip install ark-runtime-sdk[langgraph]`. No CrewAI / OpenAI-Agents adapter, no `wrap()`.
